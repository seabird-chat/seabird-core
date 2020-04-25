//go:generate protoc -I ./pb --go_out=plugins=grpc:./pb/ ./pb/seabird.proto

package seabird

import (
	"context"
	"time"

	"github.com/google/uuid"
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
func (s *Server) Register(ctx context.Context, in *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	logrus.Info("Register request")

	clientToken := uuid.New().String()
	plugin := in.Plugin

	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	if _, ok := s.clients[clientToken]; ok {
		return nil, status.Error(codes.Internal, "duplicate client token")
	}

	if _, ok := s.plugins[plugin]; ok {
		return nil, status.Error(codes.PermissionDenied, "plugin already registered")
	}

	state := &pluginState{
		name:        plugin,
		clientToken: clientToken,
		streams:     make(map[string]*streamState),
	}

	s.clients[clientToken] = plugin
	s.plugins[plugin] = state

	// Clean up any plugins which fail to register within 5 seconds
	go func() {
		time.Sleep(5 * time.Second)
		s.cleanupPlugin(state)
	}()

	return &pb.RegisterResponse{
		Identity: &pb.Identity{AuthMethod: &pb.Identity_Token{Token: clientToken}},
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

	streamId := uuid.New().String()
	inputStream := &streamState{
		broadcast: make(chan *pb.SeabirdEvent),
		commands:  make(map[string]*commandMetadata),
	}

	for _, registration := range req.Commands {
		if _, ok := inputStream.commands[registration.Name]; ok {
			return status.Error(codes.InvalidArgument, "duplicate commands")
		}

		inputStream.commands[registration.Name] = &commandMetadata{
			name:      registration.Name,
			shortHelp: registration.ShortHelp,
			fullHelp:  registration.FullHelp,
		}
	}

	// Mark this stream as active
	plugin.Lock()
	plugin.streams[streamId] = inputStream
	plugin.Unlock()

	// Ensure we properly clean up the plugin information when a plugin's last
	// event stream dies. Note that because of the defer order, stream cleanup
	// needs to be deferred last.
	defer s.clearHelpCache()
	defer s.cleanupPlugin(plugin)
	defer plugin.cleanupStream(streamId)

	for {
		select {
		case event := <-inputStream.broadcast:
			err = stream.Send(event)
			if err != nil {
				return status.Error(codes.Internal, "failed to send event")
			}
		case <-ctx.Done():
			return status.Error(codes.Canceled, "event stream cancelled")
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

	return nil, status.Error(codes.Unimplemented, "JoinChannel unimplemented")
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
