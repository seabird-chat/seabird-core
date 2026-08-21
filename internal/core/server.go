package core

import (
	"context"
	"crypto/rand"
	"log/slog"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/belak/x/slogx"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

// TODO: make these configurable using environment variables
const (
	// chatIngestBuffer is how many requests can be queued for a single
	// backend before senders start blocking.
	chatIngestBuffer = 10

	// broadcastBuffer is how many events a StreamEvents client can fall
	// behind by before it gets disconnected.
	broadcastBuffer = 32

	// requestTimeout bounds how long a backend has to answer a request.
	requestTimeout = time.Second
)

// Embedding the Unsafe server interfaces rather than the Unimplemented structs
// is deliberate: seabird-core is the canonical implementation, so a method
// missing from it should fail to compile instead of returning Unimplemented at
// runtime.
var (
	_ pb.SeabirdServer    = (*Server)(nil)
	_ pb.ChatIngestServer = (*Server)(nil)
)

// Config holds everything needed to construct a Server.
type Config struct {
	BindHost    string
	DatabaseURL string
	Logger      *slog.Logger
}

// Server is the event broker. It implements both gRPC services: ChatIngest for
// chat backends and Seabird for plugins.
type Server struct {
	pb.UnsafeSeabirdServer
	pb.UnsafeChatIngestServer

	bindHost         string
	logger           *slog.Logger
	db               *DB
	startupTimestamp uint64

	subscribersMu  sync.Mutex
	nextSubscriber uint64
	subscribers    map[uint64]*subscriber

	requestsMu sync.Mutex
	requests   map[string]chan *pb.ChatEvent

	backendsMu sync.RWMutex
	backends   map[BackendID]*chatBackend

	commandsMu sync.RWMutex
	commands   map[string]*pb.CommandMetadata
}

// subscriber is one connected StreamEvents client.
type subscriber struct {
	events chan *pb.Event
}

// chatBackend is one connected chat backend.
type chatBackend struct {
	// requests carries work from plugin RPCs to the backend's ingest stream.
	requests chan *pb.ChatRequest

	channelsMu sync.RWMutex
	channels   map[string]*pb.Channel
}

// New opens the database, applies migrations, and returns a ready Server.
func New(ctx context.Context, cfg Config) (*Server, error) {
	db, err := OpenDB(ctx, cfg.DatabaseURL, cfg.Logger)
	if err != nil {
		return nil, err
	}

	return &Server{
		bindHost:         cfg.BindHost,
		logger:           cfg.Logger,
		db:               db,
		startupTimestamp: uint64(time.Now().Unix()),
		subscribers:      make(map[uint64]*subscriber),
		requests:         make(map[string]chan *pb.ChatEvent),
		backends:         make(map[BackendID]*chatBackend),
		commands:         make(map[string]*pb.CommandMetadata),
	}, nil
}

// Close releases the database handle.
func (s *Server) Close() error {
	return s.db.Close()
}

// Run serves gRPC on the configured bind host until ctx is cancelled or the
// listener fails.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.bindHost)
	if err != nil {
		return err
	}

	return s.serve(ctx, listener)
}

func (s *Server) serve(ctx context.Context, listener net.Listener) error {
	auth := &authInterceptor{db: s.db, logger: s.logger}

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(auth.unary),
		grpc.StreamInterceptor(auth.stream),
	)

	pb.RegisterSeabirdServer(grpcServer, s)
	pb.RegisterChatIngestServer(grpcServer, s)

	// Reflection makes the server usable from grpcurl and friends. It goes
	// through the same interceptors as everything else, so a token is still
	// required to list or describe services.
	reflection.Register(grpcServer)

	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			s.logger.Info("shutting down")
			grpcServer.GracefulStop()
		case <-done:
		}
	}()

	s.logger.Info("listening", slogx.String("addr", listener.Addr().String()))

	return grpcServer.Serve(listener)
}

// subscribe registers a new StreamEvents client.
func (s *Server) subscribe() (uint64, *subscriber) {
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()

	id := s.nextSubscriber
	s.nextSubscriber++

	sub := &subscriber{events: make(chan *pb.Event, broadcastBuffer)}
	s.subscribers[id] = sub

	return id, sub
}

func (s *Server) unsubscribe(id uint64) {
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()

	delete(s.subscribers, id)
}

// broadcast fans an event out to every StreamEvents client. A client which
// can't keep up is dropped and its channel closed rather than stalling
// everyone else; StreamEvents turns the closed channel into an error so the
// client knows to reconnect.
func (s *Server) broadcast(event *pb.Event) {
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()

	for id, sub := range s.subscribers {
		select {
		case sub.events <- event:
		default:
			s.logger.Warn("dropping subscriber which fell behind", slogx.Any("subscriber", id))
			delete(s.subscribers, id)
			close(sub.events)
		}
	}
}

// registerBackend claims an ID for a newly connected backend.
func (s *Server) registerBackend(id BackendID) (*chatBackend, error) {
	s.backendsMu.Lock()
	defer s.backendsMu.Unlock()

	if _, ok := s.backends[id]; ok {
		return nil, status.Error(codes.AlreadyExists, "id already exists")
	}

	backend := &chatBackend{
		requests: make(chan *pb.ChatRequest, chatIngestBuffer),
		channels: make(map[string]*pb.Channel),
	}
	s.backends[id] = backend

	return backend, nil
}

func (s *Server) unregisterBackend(id BackendID) {
	s.backendsMu.Lock()
	defer s.backendsMu.Unlock()

	delete(s.backends, id)
}

func (s *Server) lookupBackend(id BackendID) (*chatBackend, bool) {
	s.backendsMu.RLock()
	defer s.backendsMu.RUnlock()

	backend, ok := s.backends[id]

	return backend, ok
}

// backendIDs returns the connected backend IDs in a stable order.
func (s *Server) backendIDs() []BackendID {
	s.backendsMu.RLock()
	defer s.backendsMu.RUnlock()

	ids := slices.Collect(maps.Keys(s.backends))
	slices.SortFunc(ids, func(a, b BackendID) int {
		if c := strings.Compare(a.Scheme, b.Scheme); c != 0 {
			return c
		}

		return strings.Compare(a.ID, b.ID)
	})

	return ids
}

// registerCommands inserts a plugin's commands atomically. If any name is
// already taken, none are inserted: a partial insert followed by an early
// return would leak commands into the registry permanently.
func (s *Server) registerCommands(commands map[string]*pb.CommandMetadata) error {
	s.commandsMu.Lock()
	defer s.commandsMu.Unlock()

	for name := range commands {
		if _, ok := s.commands[name]; ok {
			return status.Errorf(codes.AlreadyExists,
				"command %q already registered by another plugin", name)
		}
	}

	for name, metadata := range commands {
		s.commands[name] = metadata
	}

	return nil
}

func (s *Server) unregisterCommands(commands map[string]*pb.CommandMetadata) {
	s.commandsMu.Lock()
	defer s.commandsMu.Unlock()

	for name := range commands {
		delete(s.commands, name)
	}
}

// issueRequest sends a request to a backend and waits for the event answering
// it. The request ID is generated here and set on req.
func (s *Server) issueRequest(ctx context.Context, backendID BackendID, req *pb.ChatRequest) (*pb.ChatEvent, error) {
	backend, ok := s.lookupBackend(backendID)
	if !ok {
		return nil, status.Error(codes.NotFound, "unknown backend")
	}

	req.Id = rand.Text()

	response := make(chan *pb.ChatEvent, 1)

	s.requestsMu.Lock()
	if _, ok := s.requests[req.Id]; ok {
		s.requestsMu.Unlock()
		return nil, status.Error(codes.Internal, "failed to generate unique request ID")
	}
	s.requests[req.Id] = response
	s.requestsMu.Unlock()

	defer func() {
		s.requestsMu.Lock()
		delete(s.requests, req.Id)
		s.requestsMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	select {
	case backend.requests <- req:
	case <-ctx.Done():
		return nil, status.Error(codes.DeadlineExceeded, "request timed out")
	}

	select {
	case event := <-response:
		return event, nil
	case <-ctx.Done():
		return nil, status.Error(codes.DeadlineExceeded, "request timed out")
	}
}

// respond hands an event to whichever issueRequest call is waiting on it.
// Events with an unknown ID are ignored: the request may already have timed out.
func (s *Server) respond(id string, event *pb.ChatEvent) {
	s.requestsMu.Lock()
	response, ok := s.requests[id]
	delete(s.requests, id)
	s.requestsMu.Unlock()

	if !ok {
		return
	}

	// The channel is buffered and only ever used once, so this can't block.
	response <- event
}

// chatError converts an unexpected backend response into a gRPC error. Only
// Failed events carry a reason; anything else means the backend broke the
// protocol.
func chatError(event *pb.ChatEvent) error {
	if failed, ok := event.Inner.(*pb.ChatEvent_Failed); ok {
		return status.Error(codes.Unknown, failed.Failed.GetReason())
	}

	return status.Error(codes.Internal, "unexpected chat event")
}

// mergeTags combines the tags a client sent with the ones normalization added.
func mergeTags(tags, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(tags)+len(extra))
	maps.Copy(merged, tags)
	maps.Copy(merged, extra)

	return merged
}
