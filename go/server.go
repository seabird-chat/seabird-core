package seabird

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/seabird-irc/seabird-core/pb"
)

type ServerConfig struct {
	BindHost  string
	EnableWeb bool
	Tokens    map[string]string
}

type Server struct {
	grpcServer *grpc.Server

	configLock sync.RWMutex
	config     ServerConfig

	streamLock sync.RWMutex
	streams    map[string]*ChatStream

	eventBox           *MessageBox
	requestBox         *MessageBox
	requestResponseBox *MessageBox

	startTime time.Time
}

func NewServer(config ServerConfig) (*Server, error) {
	s := &Server{
		config:    config,
		streams:   make(map[string]*ChatStream),
		startTime: time.Now(),
		// 10 seems like a reasonable number, don't you think? It may be
		// worthwhile looking into a more robust solution for this.
		eventBox:   NewMessageBox(10),
		requestBox: NewMessageBox(10),
		// We only need a box size of 1 for the request-response box because
		// there will only ever be one response.
		requestResponseBox: NewMessageBox(1),
	}
	s.SetTokens(config.Tokens)

	s.grpcServer = grpc.NewServer(
		grpc.ChainUnaryInterceptor(s.unaryAuthInterceptor),
		grpc.ChainStreamInterceptor(s.streamAuthInterceptor),
	)
	pb.RegisterSeabirdServer(s.grpcServer, s)
	pb.RegisterChatIngestServer(s.grpcServer, s)

	return s, nil
}

func (s *Server) NewChatStream(streamType, id string, doneCallback func()) (*ChatStream, error) {
	s.streamLock.Lock()
	defer s.streamLock.Unlock()

	fullStreamID := (&url.URL{
		Scheme: streamType,
		Host:   id,
	}).String()

	if s.streams[fullStreamID] != nil {
		return nil, status.Error(codes.AlreadyExists, "stream already exists")
	}

	cs, err := NewChatStream(streamType, id, s, doneCallback)
	if err != nil {
		return nil, err
	}

	s.streams[fullStreamID] = cs

	return cs, nil
}

func (s *Server) removeStream(cs *ChatStream) {
	s.streamLock.Lock()
	defer s.streamLock.Unlock()

	delete(s.streams, cs.GetID())
}

func (s *Server) LookupStream(id string) *ChatStream {
	s.streamLock.RLock()
	defer s.streamLock.RUnlock()

	return s.streams[id]
}

// SetTokens allows an external method to update the tokens of the currently
// running server to avoid having to restart when they are added.
func (s *Server) SetTokens(tokens map[string]string) {
	s.configLock.Lock()
	defer s.configLock.Unlock()

	s.config.Tokens = tokens
}

func (s *Server) authenticate(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing request metadata")
	}

	authTokens := md.Get("authorization")
	switch len(authTokens) {
	case 1:
		// This is the valid case, so we don't need to do anything else here.
	case 0:
		return status.Errorf(codes.Unauthenticated, "missing auth token")
	default:
		return status.Errorf(codes.Unauthenticated, "wrong number of auth tokens: got %d, expected 1", len(authTokens))
	}

	authToken := authTokens[0]
	if !strings.HasPrefix(authToken, "Bearer ") {
		return status.Error(codes.Unauthenticated, "invalid token format")
	}
	authToken = strings.TrimPrefix(authToken, "Bearer ")

	s.configLock.RLock()
	defer s.configLock.RUnlock()

	if s.config.Tokens[authToken] == "" {
		return status.Error(codes.Unauthenticated, "invalid auth token")
	}

	return nil
}

func (s *Server) unaryAuthInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}

	fmt.Println(info.FullMethod)

	return handler(ctx, req)
}

func (s *Server) streamAuthInterceptor(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	err := s.authenticate(stream.Context())
	if err != nil {
		return err
	}

	fmt.Println(info.FullMethod)

	return handler(srv, stream)
}

func (s *Server) Run() error {
	wrapped := grpcweb.WrapServer(
		s.grpcServer,
		grpcweb.WithWebsockets(true),
		grpcweb.WithWebsocketPingInterval(30*time.Second),

		// We allow all origins because there's other auth
		grpcweb.WithOriginFunc(func(origin string) bool { return true }),
		grpcweb.WithWebsocketOriginFunc(func(req *http.Request) bool { return true }),
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If web is enabled and this is a valid grpc-web request, send it to
		// the grpc-web handler.
		if s.config.EnableWeb && (wrapped.IsGrpcWebRequest(r) || wrapped.IsGrpcWebSocketRequest(r) || wrapped.IsAcceptableGrpcCorsRequest(r)) {
			wrapped.ServeHTTP(w, r)
			return
		}

		// We use this to determine if the gRPC handler should be called. This
		// is the recommended example from gRPC's ServeHTTP.
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			s.grpcServer.ServeHTTP(w, r)
			return
		}

		// Anything can be put after this point - you could even serve a full
		// website from here. We use StatusNotFound because this is just gRPC.
		w.WriteHeader(http.StatusNotFound)
	})

	// Wrap the handler with h2c so we can use TLS termination with a remote
	// proxy.
	return http.ListenAndServe(s.config.BindHost, h2c.NewHandler(handler, &http2.Server{}))
}
