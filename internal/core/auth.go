package core

import (
	"context"
	"log/slog"
	"strings"

	"github.com/belak/x/slogx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type authUsernameKey struct{}

// authUsername returns the username the interceptor attached to this request.
func authUsername(ctx context.Context) (string, error) {
	username, ok := ctx.Value(authUsernameKey{}).(string)
	if !ok {
		return "", status.Error(codes.Internal, "missing auth username")
	}

	return username, nil
}

// authInterceptor authenticates every RPC against the auth token table and
// attaches the token's name to the request context as the calling username.
type authInterceptor struct {
	db     *DB
	logger *slog.Logger
}

func (a *authInterceptor) unary(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	ctx, err := a.authenticate(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}

	return handler(ctx, req)
}

func (a *authInterceptor) stream(
	srv any,
	stream grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	ctx, err := a.authenticate(stream.Context(), info.FullMethod)
	if err != nil {
		return err
	}

	return handler(srv, &authedStream{ServerStream: stream, ctx: ctx})
}

func (a *authInterceptor) authenticate(ctx context.Context, method string) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing authorization header")
	}

	values := md.Get("authorization")
	if len(values) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization header")
	}

	scheme, key, ok := strings.Cut(values[0], " ")
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing auth token")
	}

	if scheme != "Bearer" {
		return nil, status.Error(codes.Unauthenticated, "unknown auth method")
	}

	token, err := a.db.GetAuthToken(ctx, key)
	if err != nil {
		a.logger.Error("failed to look up auth token", slogx.Err(err))
		return nil, status.Error(codes.Unauthenticated, "invalid auth token")
	}

	if token == nil {
		return nil, status.Error(codes.Unauthenticated, "invalid auth token")
	}

	a.logger.Info("authenticated request",
		slogx.String("user", token.Name),
		slogx.String("method", method))

	return context.WithValue(ctx, authUsernameKey{}, token.Name), nil
}

// authedStream overrides the context seen by a streaming handler so it can read
// the authenticated username.
type authedStream struct {
	grpc.ServerStream

	ctx context.Context
}

func (s *authedStream) Context() context.Context {
	return s.ctx
}
