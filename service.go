//go:generate protoc -I ./pb --go_out=plugins=grpc:./pb/ ./pb/seabird.proto

package seabird

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	irc "gopkg.in/irc.v3"

	"github.com/belak/seabird-core/pb"
)

// TODO: move this into a test
var _ pb.SeabirdServer = &Server{}

// TODO: add tests

// Register is the internal implementation of SeabirdServer.Register
func (s *Server) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	logrus.Info("Register request")

	commands := make(map[string]*commandMetadata)
	for _, registration := range req.Commands {
		if _, ok := commands[registration.Name]; ok {
			return nil, status.Error(codes.InvalidArgument, "duplicate commands")
		}

		commands[registration.Name] = &commandMetadata{
			name:      registration.Name,
			shortHelp: registration.ShortHelp,
			fullHelp:  registration.FullHelp,
		}
	}

	plugin := NewPlugin(req.Plugin, commands)

	err := s.AddPlugin(plugin)
	if err != nil {
		return nil, err
	}

	// Clean up any plugins which fail to register within 5 seconds.
	go func() {
		time.Sleep(5 * time.Second)
		s.MaybeRemovePlugin(plugin)
	}()

	return &pb.RegisterResponse{
		Identity: &pb.Identity{AuthMethod: &pb.Identity_Token{Token: plugin.Token}},
	}, nil
}

// EventStream is the internal implementation of SeabirdServer.EventStream
func (s *Server) EventStream(req *pb.EventStreamRequest, stream pb.Seabird_EventStreamServer) error {
	logrus.Info("EventStream request")

	ctx := stream.Context()
	plugin, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return err
	}

	inputStream := NewStream()
	err = plugin.AddStream(inputStream)
	if err != nil {
		return err
	}

	// Ensure we properly clean up the plugin information when a plugin's last
	// event stream dies. Note that because of the defer order, stream cleanup
	// needs to be deferred last.
	defer s.MaybeRemovePlugin(plugin)
	defer plugin.RemoveStream(inputStream)

	for {
		event, err := inputStream.Recv(ctx)
		if err != nil {
			return status.Error(codes.Canceled, "event stream cancelled")
		}

		err = stream.Send(event)
		if err != nil {
			return status.Error(codes.Internal, "failed to send event")
		}
	}
}

// SendMessage is the internal implementation of SeabirdServer.SendMessage
func (s *Server) SendMessage(ctx context.Context, req *pb.SendMessageRequest) (*pb.SendMessageResponse, error) {
	logrus.Info("SendMessage request")

	_, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return nil, err
	}

	err = s.client.WriteMessage(&irc.Message{
		Command: "PRIVMSG",
		Params:  []string{req.Target, req.Message},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.SendMessageResponse{}, nil
}

// SendRawMessage is the internal implementation of SeabirdServer.SendMessage
func (s *Server) SendRawMessage(ctx context.Context, req *pb.SendRawMessageRequest) (*pb.SendRawMessageResponse, error) {
	logrus.Info("SendMessage request")

	_, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return nil, err
	}

	err = s.client.WriteMessage(&irc.Message{
		Command: req.Command,
		Params:  req.Params,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.SendRawMessageResponse{}, nil
}

// GetChannelInfo is the internal implementation of SeabirdServer.GetChannelInfo
func (s *Server) ListChannels(ctx context.Context, req *pb.ListChannelsRequest) (*pb.ListChannelsResponse, error) {
	logrus.Info("ListChannels request")

	_, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return nil, err
	}

	resp := &pb.ListChannelsResponse{Names: s.tracker.ListChannels()}

	return resp, nil
}

// GetChannelInfo is the internal implementation of SeabirdServer.GetChannelInfo
func (s *Server) GetChannelInfo(ctx context.Context, req *pb.ChannelInfoRequest) (*pb.ChannelInfoResponse, error) {
	logrus.Info("GetChannelInfo request")

	_, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return nil, err
	}

	channel := s.tracker.GetChannel(req.Name)
	if channel == nil {
		return nil, status.Error(codes.NotFound, "channel not found")
	}

	resp := &pb.ChannelInfoResponse{Name: channel.Name, Topic: channel.Topic, Users: nil}
	for nick := range channel.Users {
		resp.Users = append(resp.Users, &pb.User{Nick: nick})
	}

	return resp, nil
}

// SetChannelInfo is the internal implementation of SeabirdServer.SetChannelInfo
func (s *Server) SetChannelInfo(ctx context.Context, req *pb.SetChannelInfoRequest) (*pb.SetChannelInfoResponse, error) {
	logrus.Info("SetChannelInfo request")

	_, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return nil, err
	}

	channel := s.tracker.GetChannel(req.Name)
	if channel == nil {
		return nil, status.Error(codes.NotFound, "channel not found")
	}

	// TODO: figure out how to clear the topic
	if req.Topic != "" {
		err := s.client.WriteMessage(&irc.Message{
			Command: "TOPIC",
			Params:  []string{req.Name, req.Topic},
		})
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to write message")
		}
	}

	return &pb.SetChannelInfoResponse{}, nil
}

// JoinChannel is the internal implementation of SeabirdServer.JoinChannel
func (s *Server) JoinChannel(ctx context.Context, req *pb.JoinChannelRequest) (*pb.JoinChannelResponse, error) {
	logrus.Info("JoinChannel request")

	_, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return nil, err
	}

	err = s.client.WriteMessage(&irc.Message{
		Command: "JOIN",
		Params:  []string{req.Target},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	// TODO: this is implemented
	return &pb.JoinChannelResponse{}, nil
}

// LeaveChannel is the internal implementation of SeabirdServer.LeaveChannel
func (s *Server) LeaveChannel(ctx context.Context, req *pb.LeaveChannelRequest) (*pb.LeaveChannelResponse, error) {
	logrus.Info("LeaveChannel request")

	_, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return nil, err
	}

	err = s.client.WriteMessage(&irc.Message{
		Command: "PART",
		Params:  []string{req.Target, req.Message},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return nil, status.Error(codes.Unimplemented, "LeaveChannel unimplemented")
}

func (s *Server) ListPlugins(ctx context.Context, req *pb.ListPluginsRequest) (*pb.ListPluginsResponse, error) {
	logrus.Info("ListPlugins request")

	_, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return nil, err
	}

	s.pluginLock.RLock()
	defer s.pluginLock.RUnlock()

	var plugins []string
	for plugin := range s.plugins {
		plugins = append(plugins, plugin)
	}

	resp := &pb.ListPluginsResponse{Names: plugins}

	return resp, nil
}

func (s *Server) GetPluginInfo(ctx context.Context, req *pb.PluginInfoRequest) (*pb.PluginInfoResponse, error) {
	logrus.Info("GetPluginInfo request")

	_, err := s.lookupPlugin(req.Identity)
	if err != nil {
		return nil, err
	}

	s.pluginLock.RLock()
	defer s.pluginLock.RUnlock()

	plugin, ok := s.plugins[req.Name]
	if !ok {
		return nil, status.Error(codes.NotFound, "plugin not found")
	}

	resp := &pb.PluginInfoResponse{Name: plugin.Name, Commands: make(map[string]*pb.CommandMetadata)}

	for _, command := range plugin.commands {
		resp.Commands[command.name] = &pb.CommandMetadata{
			Name:      command.name,
			ShortHelp: command.shortHelp,
			FullHelp:  command.fullHelp,
		}
	}

	return resp, nil
}
