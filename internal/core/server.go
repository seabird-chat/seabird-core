package core

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc"
)

var (
	_ pb.SeabirdServer    = (*Server)(nil)
	_ pb.ChatIngestServer = (*Server)(nil)
)

const (
	// DefaultRequestTimeout is how long a request to a backend waits for its
	// reply unless Server.RequestTimeout says otherwise.
	DefaultRequestTimeout = time.Second

	// DefaultHelloTimeout is how long a backend has to introduce itself unless
	// Server.HelloTimeout says otherwise.
	DefaultHelloTimeout = 5 * time.Second
)

// Server implements the plugin and backend gRPC services. DB and Broker are
// required; the other fields have defaults.
type Server struct {
	// These are the canonical servers, so the Unsafe embeds turn a missing
	// method into a compile error instead of an Unimplemented fallback.
	pb.UnsafeSeabirdServer
	pb.UnsafeChatIngestServer

	DB     *DB
	Broker EventBroker

	// Logger defaults to slog.Default().
	Logger *slog.Logger

	// RequestTimeout is how long requests to backends wait for a reply, and
	// HelloTimeout is how long a new backend has to introduce itself. Zero
	// means the matching default.
	RequestTimeout time.Duration
	HelloTimeout   time.Duration

	backends backendRegistry
	commands commandRegistry

	// startedAt is when Serve began.
	startedAt time.Time
}

// Serve handles gRPC requests until ctx is canceled or the listener fails. On
// cancellation it stops immediately, canceling in-flight RPCs, and returns once
// their handlers have exited.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if s.DB == nil || s.Broker == nil {
		return errors.New("server needs a DB and a Broker")
	}
	s.startedAt = time.Now()

	server := grpc.NewServer(
		grpc.UnaryInterceptor(s.serveUnary),
		grpc.StreamInterceptor(s.serveStream),
		// Makes Stop wait for handlers, so their cleanup is done when Serve
		// returns.
		grpc.WaitForHandlers(true),
	)
	pb.RegisterSeabirdServer(server, s)
	pb.RegisterChatIngestServer(server, s)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// There are almost always open event streams, so waiting for RPCs to finish
	// would only delay shutdown. Clients see the connection drop.
	server.Stop()

	err := <-serveErr
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

func (s *Server) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

func (s *Server) requestTimeout() time.Duration {
	return cmp.Or(s.RequestTimeout, DefaultRequestTimeout)
}

func (s *Server) helloTimeout() time.Duration {
	return cmp.Or(s.HelloTimeout, DefaultHelloTimeout)
}
