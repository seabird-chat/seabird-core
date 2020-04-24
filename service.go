package seabird

import (
	"context"
	"time"

	"github.com/belak/seabird-core/pb"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	irc "gopkg.in/irc.v3"
)

// Register is the internal implementation of SeabirdServer.Register
func (s *Server) Register(ctx context.Context, in *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	clientId := uuid.New().String()
	plugin := in.Identifier

	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	if _, ok := s.plugins[plugin]; ok {
		return nil, status.Error(codes.PermissionDenied, "plugin already registered")
	}

	state := &pluginState{name: plugin, clientId: clientId}

	s.plugins[plugin] = state

	// Clean up any plugins which fail to register within 5 seconds
	defer func() {
		go func() {
			time.Sleep(5 * time.Second)

			state.Lock()
			defer state.Unlock()

			if state.broadcast == nil {
				s.pluginLock.Lock()
				defer s.pluginLock.Unlock()

				delete(s.plugins, state.name)
			}
		}()
	}()

	return &pb.RegisterResponse{ClientId: clientId}, nil
}

// EventStream is the internal implementation of SeabirdServer.EventStream
func (s *Server) EventStream(in *pb.EventStreamRequest, stream pb.Seabird_EventStreamServer) error {
	ctx := stream.Context()
	plugin, err := s.lookupPlugin(ctx)
	if err != nil {
		return err
	}

	plugin.Lock()

	if plugin.broadcast != nil {
		plugin.Unlock()
		return status.Error(codes.PermissionDenied, "plugin event stream already started")
	}

	plugin.broadcast = make(chan *pb.SeabirdEvent)
	plugin.Unlock()

	// Ensure we properly clean up the plugin information when a plugin's event stream dies.
	defer func() {
		s.pluginLock.Lock()
		defer s.pluginLock.Unlock()

		delete(s.plugins, plugin.name)
	}()

	for {
		select {
		case event := <-plugin.broadcast:
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
	_, err := s.lookupPlugin(ctx)
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

// SendReplyMessage is the internal implementation of SeabirdServer.SendReplyMessage
func (s *Server) SendReplyMessage(ctx context.Context, req *pb.SendReplyMessageRequest) (*pb.SendReplyMessageResponse, error) {
	_, err := s.lookupPlugin(ctx)
	if err != nil {
		return nil, err
	}

	switch event := req.Target.Event.(type) {
	case (*pb.SeabirdEvent_Message):
		if req.Mention {
			err = s.client.Writef("PRIVMSG %s :%s: %s", event.Message.Channel, event.Message.Sender, req.Message)
		} else {
			err = s.client.Writef("PRIVMSG %s :%s", event.Message.Channel, req.Message)
		}
	case (*pb.SeabirdEvent_Command):
		if req.Mention {
			err = s.client.Writef("PRIVMSG %s :%s: %s", event.Command.Channel, event.Command.Sender, req.Message)
		} else {
			err = s.client.Writef("PRIVMSG %s :%s", event.Command.Channel, req.Message)
		}
	case (*pb.SeabirdEvent_Mention):
		if req.Mention {
			err = s.client.Writef("PRIVMSG %s :%s: %s", event.Mention.Channel, event.Mention.Sender, req.Message)
		} else {
			err = s.client.Writef("PRIVMSG %s :%s", event.Mention.Channel, req.Message)
		}
	case (*pb.SeabirdEvent_PrivateMessage):
		err = s.client.Writef("PRIVMSG %s :%s", event.PrivateMessage.Sender, req.Message)
	default:
		return nil, status.Error(codes.Internal, "unknown event type")
	}

	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.SendReplyMessageResponse{}, nil
}

// GetChannel is the internal implementation of SeabirdServer.GetChannel
func (s *Server) GetChannel(ctx context.Context, req *pb.ChannelRequest) (*pb.ChannelResponse, error) {
	_, err := s.lookupPlugin(ctx)
	if err != nil {
		return nil, err
	}

	channel := s.tracker.GetChannel(req.Name)
	if channel == nil {
		return nil, status.Error(codes.NotFound, "channel not found")
	}

	resp := &pb.ChannelResponse{Name: channel.Name, Topic: channel.Topic, Users: nil}
	for nick := range channel.Users {
		resp.Users = append(resp.Users, &pb.User{Nick: nick})
	}

	return resp, nil
}

// JoinChannel is the internal implementation of SeabirdServer.JoinChannel
func (s *Server) JoinChannel(ctx context.Context, req *pb.JoinChannelRequest) (*pb.JoinChannelResponse, error) {
	_, err := s.lookupPlugin(ctx)
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
	_, err := s.lookupPlugin(ctx)
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
