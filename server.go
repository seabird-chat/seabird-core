package seabird

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/irc.v3"

	"github.com/seabird-irc/seabird-core/ircx"
	"github.com/seabird-irc/seabird-core/pb"
)

type ServerConfig struct {
	IrcHost       string            `json:"irc_host"`
	Nick          string            `json:"irc_nick"`
	User          string            `json:"irc_user"`
	Name          string            `json:"irc_name"`
	Pass          string            `json:"irc_pass"`
	CommandPrefix string            `json:"command_prefix"`
	BindHost      string            `json:"bind_host"`
	EnableWeb     bool              `json:"enable_web"`
	Tokens        map[string]string `json:"tokens"`
}

type Server struct {
	grpcServer *grpc.Server
	// TODO: client should perhaps be put behind a mutex
	client  *irc.Client
	tracker *ircx.Tracker

	configLock sync.RWMutex
	config     ServerConfig

	streamLock sync.RWMutex
	streams    map[uuid.UUID]*EventStream

	startTime time.Time
}

func NewServer(config ServerConfig) (*Server, error) {
	s := &Server{
		tracker:   ircx.NewTracker(),
		config:    config,
		streams:   make(map[uuid.UUID]*EventStream),
		startTime: time.Now(),
	}
	s.SetTokens(config.Tokens)

	ircUrl, err := url.Parse(config.IrcHost)
	if err != nil {
		return nil, err
	}

	hostname := ircUrl.Hostname()
	port := ircUrl.Port()

	var c io.ReadWriteCloser

	switch ircUrl.Scheme {
	case "irc":
		if port == "" {
			port = "6667"
		}

		c, err = net.Dial("tcp", fmt.Sprintf("%s:%s", hostname, port))
	case "ircs":
		if port == "" {
			port = "6697"
		}

		c, err = tls.Dial("tcp", fmt.Sprintf("%s:%s", hostname, port), nil)
	case "ircs+unsafe":
		if port == "" {
			port = "6697"
		}

		c, err = tls.Dial("tcp", fmt.Sprintf("%s:%s", hostname, port), &tls.Config{
			InsecureSkipVerify: true,
		})
	default:
		return nil, fmt.Errorf("unknown irc scheme %s", ircUrl.Scheme)
	}

	if err != nil {
		return nil, err
	}

	s.client = irc.NewClient(c, irc.ClientConfig{
		Nick:          config.Nick,
		User:          config.User,
		Name:          config.Name,
		Pass:          config.Pass,
		PingFrequency: 60 * time.Second,
		PingTimeout:   10 * time.Second,
		Handler:       irc.HandlerFunc(s.ircHandler),
	})

	s.grpcServer = grpc.NewServer()
	pb.RegisterSeabirdServer(s.grpcServer, s)

	return s, nil
}

// SetTokens allows an external method to update the tokens of the currently
// running server to avoid having to restart when they are added.
func (s *Server) SetTokens(tokens map[string]string) {
	s.configLock.Lock()
	defer s.configLock.Unlock()

	// Note that we need to reverse the order of the tokens because the config
	// has it in the more human readable user : token, but we need it the other
	// way around.
	s.config.Tokens = make(map[string]string)
	for k, v := range tokens {
		s.config.Tokens[v] = k
	}
}

// Run will connect to the IRC server and start the listener.
func (s *Server) Run() error {
	group, ctx := errgroup.WithContext(context.Background())
	group.Go(func() error {
		wrapped := grpcweb.WrapServer(
			s.grpcServer,
			grpcweb.WithWebsockets(true),
			grpcweb.WithWebsocketPingInterval(30*time.Second),

			// We allow all origins because there's other auth
			grpcweb.WithOriginFunc(func(origin string) bool { return true }),
			grpcweb.WithWebsocketOriginFunc(func(req *http.Request) bool { return true }),
		)

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If web is enabled and this is a valid grpc-web request, send it
			// to the grpc-web handler.
			if s.config.EnableWeb && (wrapped.IsGrpcWebRequest(r) || wrapped.IsGrpcWebSocketRequest(r) || wrapped.IsAcceptableGrpcCorsRequest(r)) {
				wrapped.ServeHTTP(w, r)
				return
			}

			// This is the recommended example from gRPC's ServeHTTP.
			if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
				s.grpcServer.ServeHTTP(w, r)
				return
			}

			w.WriteHeader(http.StatusNotFound)
		})

		// We can't use http.ListenAndServe because we want to set a base
		// context.
		server := http.Server{
			Addr:        s.config.BindHost,
			BaseContext: func(net.Listener) context.Context { return ctx },

			// We need to wrap our handler in h2c because we require some proxy
			// to encrypt it.
			Handler: h2c.NewHandler(handler, &http2.Server{}),
		}

		return server.ListenAndServe()
	})

	group.Go(func() error {
		return s.client.RunContext(ctx)
	})

	return group.Wait()
}

func (s *Server) NewStream(ctx context.Context, addr net.Addr, meta map[string]*CommandMetadata) (context.Context, *EventStream) {
	s.streamLock.Lock()
	defer s.streamLock.Unlock()

	ret := NewEventStream(ctx, addr, meta)
	s.streams[ret.ID()] = ret

	return WithStreamID(ctx, ret.ID()), ret
}

func (s *Server) verifyIdentity(endpointName string, identity *pb.Identity) (*logrus.Entry, error) {
	logger := logrus.WithFields(logrus.Fields{
		"endpoint":   endpointName,
		"request_id": uuid.New().String(),
	})
	logger.Debug("request started")

	s.configLock.RLock()
	defer s.configLock.RUnlock()

	switch v := identity.GetAuthMethod().(type) {
	case *pb.Identity_Token:
		tag, ok := s.config.Tokens[v.Token]
		if !ok {
			return logger, status.Error(codes.Unauthenticated, "invalid auth token")
		}

		logger = logger.WithField("tag", tag)
		logger.Info("request authenticated")

		return logger, nil
	default:
		return logger, status.Error(codes.Unauthenticated, "invalid auth method")
	}
}
