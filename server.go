package seabird

import (
	"crypto/tls"
	"net"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	irc "gopkg.in/irc.v3"

	"github.com/belak/seabird-core/pb"
)

type Server struct {
	client     *irc.Client
	grpcServer *grpc.Server

	config ServerConfig

	pluginLock sync.RWMutex
	clients    map[string]string       // Mapping of client_id to plugin name
	plugins    map[string]*pluginState // Mapping of plugin name to state

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
	sync.RWMutex

	name        string
	clientToken string
	streams     map[string]*streamState

	// TODO: do something with this metric
	consecutiveDroppedMessages int
}

type streamState struct {
	broadcast chan *pb.SeabirdEvent
	commands  map[string]*commandMetadata
}

type commandMetadata struct {
	name      string
	shortHelp string
	fullHelp  string
}

func (p *pluginState) cleanupStream(streamId string) {
	p.Lock()
	defer p.Unlock()

	delete(p.streams, streamId)
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
		clients: make(map[string]string),
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

func (s *Server) lookupPlugin(identity *pb.Identity) (*pluginState, error) {
	clientToken := identity.GetToken()

	if identity.GetToken() == "" {
		return nil, status.Error(codes.Unauthenticated, "Identity missing token")
	}

	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	plugin := s.clients[clientToken]
	meta := s.plugins[plugin]

	if meta == nil {
		return nil, status.Error(codes.Unauthenticated, "invalid client_token")
	}

	return meta, nil
}

func (s *Server) cleanupPlugin(plugin *pluginState) {
	plugin.RLock()
	if len(plugin.streams) != 0 {
		plugin.RUnlock()
		return
	}
	plugin.RUnlock()

	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	clientToken := plugin.clientToken

	delete(s.plugins, s.clients[clientToken])
	delete(s.clients, clientToken)
}

func (s *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", s.config.BindHost)
	if err != nil {
		return err
	}

	return s.grpcServer.Serve(listener)
}
