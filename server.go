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
	clients    map[string]string  // Mapping of client_id to plugin name
	plugins    map[string]*Plugin // Mapping of plugin name to state

	tracker *Tracker

	helpLock          sync.Mutex
	helpCacheCommands []string
	helpCacheMetadata map[string][]*commandMetadata
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

type commandMetadata struct {
	name      string
	shortHelp string
	fullHelp  string
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
		plugins: make(map[string]*Plugin),
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

func (s *Server) AddPlugin(plugin *Plugin) error {
	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	if _, ok := s.clients[plugin.Token]; ok {
		return status.Error(codes.Internal, "duplicate client token")
	}

	if _, ok := s.plugins[plugin.Name]; ok {
		return status.Error(codes.PermissionDenied, "plugin already registered")
	}

	// Register this plugin on the server
	s.clients[plugin.Token] = plugin.Name
	s.plugins[plugin.Name] = plugin

	return nil
}

func (s *Server) MaybeRemovePlugin(plugin *Plugin) {
	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	if !plugin.Dead() {
		return
	}

	s.removePlugin(plugin)
}

func (s *Server) DropPlugin(plugin *Plugin) {
	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	plugin.Close()

	s.removePlugin(plugin)
}

func (s *Server) removePlugin(plugin *Plugin) {
	// NOTE: pluginLock needs to be acquired while this is called.

	clientToken := plugin.Token

	delete(s.plugins, s.clients[clientToken])
	delete(s.clients, clientToken)

	// We need to do this in a goroutine to avoid a possible deadlock. This
	// allows it to be delayed until the helpLock is available.
	go func() {
		s.clearHelpCache()
	}()
}

func (s *Server) lookupPlugin(identity *pb.Identity) (*Plugin, error) {
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

func (s *Server) clearHelpCache() {
	s.helpLock.Lock()
	defer s.helpLock.Unlock()

	s.helpCacheCommands = nil
	s.helpCacheMetadata = nil
}

func (s *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", s.config.BindHost)
	if err != nil {
		return err
	}

	return s.grpcServer.Serve(listener)
}
