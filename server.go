package seabird

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/belak/seabird-core/pb"
	"github.com/sirupsen/logrus"
	irc "gopkg.in/irc.v3"
)

type Server struct {
	client     *irc.Client
	grpcServer *grpc.Server

	config ServerConfig

	pluginLock sync.RWMutex
	plugins    map[string]*pluginState

	tracker *Tracker
}

type ServerConfig struct {
	IrcHost       string
	CommandPrefix string
	BindHost      string
	Nick          string
	User          string
	Name          string
	Pass          string
}

type pluginState struct {
	sync.Mutex

	name      string
	clientId  string
	broadcast chan *pb.SeabirdEvent

	// TODO: do something with this metric
	droppedMessages int
}

func NewServer(config ServerConfig) (*Server, error) {
	var c net.Conn
	var err error

	if strings.HasPrefix(config.IrcHost, "+") {
		c, err = tls.Dial("tcp", config.IrcHost[1:], nil)
	} else {
		c, err = net.Dial("tcp", config.IrcHost)
	}

	if err != nil {
		return nil, err
	}

	s := &Server{
		plugins: make(map[string]*pluginState),
		tracker: NewTracker(),

		config: config,
	}

	client := irc.NewClient(c, irc.ClientConfig{
		Nick:    config.Nick,
		User:    config.User,
		Name:    config.Name,
		Pass:    config.Pass,
		Handler: irc.HandlerFunc(s.ircHandler),
	})

	s.client = client

	s.grpcServer = grpc.NewServer()
	pb.RegisterSeabirdServer(s.grpcServer, s)

	// TODO: properly handle this error
	go func() {
		err := client.Run()
		if err != nil {
			logrus.WithError(err).Fatal("IRC connection exited")
		}
	}()

	return s, nil
}

func (s *Server) lookupPlugin(ctx context.Context) (*pluginState, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md["client_id"]) != 1 || len(md["plugin"]) != 1 {
		return nil, status.Error(codes.Unauthenticated, "missing client_id or plugin metadata")
	}

	clientId := md["client_id"][0]
	plugin := md["plugin"][0]

	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	meta := s.plugins[plugin]

	if meta == nil || meta.clientId != clientId {
		return nil, status.Error(codes.Unauthenticated, "plugin has not been registered or invalid client_id")
	}

	return meta, nil
}

func (s *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", s.config.BindHost)
	if err != nil {
		return err
	}

	return s.grpcServer.Serve(listener)
}
