package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/belak/x/slogx"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// serveUnary is the server's only unary interceptor. gRPC allows one per server,
// and it runs every unary RPC through serveRPC.
func (s *Server) serveUnary(
	ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	var resp any
	err := s.serveRPC(ctx, info.FullMethod, func(ctx context.Context) error {
		var err error
		resp, err = handler(ctx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// serveStream is the server's only stream interceptor, the streaming
// counterpart of serveUnary.
func (s *Server) serveStream(
	srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) error {
	return s.serveRPC(stream.Context(), info.FullMethod, func(ctx context.Context) error {
		return handler(srv, &streamWithContext{ServerStream: stream, ctx: ctx})
	})
}

// serveRPC authenticates the request, runs call, and logs the outcome. The
// context passed to call carries the username and a logger for the RPC.
//
// Handlers return either a gRPC status error, which is sent to the client as
// is, or any other error, which is reported to the client as a generic Internal
// status. Wrap causes with context and return them; don't log them, since every
// failed RPC is logged here.
func (s *Server) serveRPC(
	ctx context.Context, method string, call func(ctx context.Context) error,
) error {
	start := time.Now()
	logger := s.logger().With(slogx.String("method", method))

	// The peer is the only way to tell clients apart before they authenticate.
	// Behind a proxy, it is the proxy's address.
	if p, ok := peer.FromContext(ctx); ok {
		logger = logger.With(slogx.String("peer", p.Addr.String()))
	}

	username, err := s.authenticate(ctx)
	if err == nil {
		logger = logger.With(slogx.String("username", username))
		ctx = context.WithValue(ctx, usernameContextKey{}, username)
		ctx = slogx.WithLogger(ctx, logger)

		logger.DebugContext(ctx, "rpc started")
		err = call(ctx)
	}

	if err == nil {
		logger.DebugContext(ctx, "rpc finished",
			slogx.String("code", codes.OK.String()),
			slogx.Duration("duration", time.Since(start)),
		)
		return nil
	}

	clientErr := err
	st, isStatus := status.FromError(err)
	if !isStatus {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			st = status.FromContextError(err)
		} else {
			st = status.New(codes.Internal, "internal error")
		}
		clientErr = st.Err()
	}

	logger.LogAttrs(ctx, failedRPCLogLevel(method, st.Code()).Level(), "rpc failed",
		slogx.String("code", st.Code().String()),
		slogx.Duration("duration", time.Since(start)),
		slogx.Err(err),
	)
	return clientErr
}

// failedRPCLogLevel picks the level for a failed RPC. Server faults are errors, and
// rejected credentials are info since they usually mean a misconfigured
// client. A backend's stream ending for any other reason but a disconnect is a
// warning: it's a backend bug or misconfiguration that repeats on every
// reconnect. Everything else is routine for plugins, so it's debug.
func failedRPCLogLevel(method string, code codes.Code) slogx.Level {
	switch {
	case code == codes.Internal, code == codes.DataLoss:
		return slogx.LevelError
	case code == codes.Unauthenticated:
		return slogx.LevelInfo
	case method == pb.ChatIngest_IngestEvents_FullMethodName && code != codes.Canceled:
		return slogx.LevelWarn
	default:
		return slogx.LevelDebug
	}
}

// streamWithContext replaces a stream's context, so stream handlers see what
// serveRPC added to it. grpc-go has no helper for this, though
// go-grpc-middleware's WrappedServerStream does the same.
type streamWithContext struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *streamWithContext) Context() context.Context {
	return s.ctx
}

type usernameContextKey struct{}

// authUsername returns the username the RPC interceptors attached to ctx.
func authUsername(ctx context.Context) (string, error) {
	username, ok := ctx.Value(usernameContextKey{}).(string)
	if !ok {
		return "", status.Error(codes.Internal, "missing auth username")
	}
	return username, nil
}

// authenticate validates the bearer token in the request metadata and returns
// the username it belongs to. Rejections are gRPC statuses meant for the
// client; any other error is an internal failure.
func (s *Server) authenticate(ctx context.Context) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)

	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}
	if len(values) > 1 {
		// Something in between may have added one, so neither can be trusted.
		return "", status.Error(codes.Unauthenticated, "multiple authorization headers")
	}

	scheme, key, hasKey := strings.Cut(values[0], " ")
	if scheme != "Bearer" {
		return "", status.Error(codes.Unauthenticated, "unknown auth method")
	}
	if !hasKey {
		return "", status.Error(codes.Unauthenticated, "missing auth token")
	}

	token, err := s.DB.GetAuthToken(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return "", status.Error(codes.Unauthenticated, "invalid auth token")
	}
	if err != nil {
		return "", fmt.Errorf("look up auth token: %w", err)
	}

	return token.Name, nil
}
