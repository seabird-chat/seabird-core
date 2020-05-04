package seabird

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	irc "gopkg.in/irc.v3"

	"github.com/seabird-irc/seabird-core/pb"
)

// Ensure that Server implements the SeabirdServer interface
var _ pb.SeabirdServer = &Server{}

func (s *Server) StreamEvents(req *pb.StreamEventsRequest, outputStream pb.Seabird_StreamEventsServer) error {
	logger, err := s.verifyIdentity("StreamEvents", req.Identity)
	if err != nil {
		return err
	}

	meta := make(map[string]*CommandMetadata)
	for k, v := range req.Commands {
		meta[k] = &CommandMetadata{Name: k, ShortHelp: v.ShortHelp, FullHelp: v.FullHelp}
	}

	// NOTE: we do the setup slightly different here in order to have the
	// stream_id attached properly to the logger.
	ctx, eventStream := s.NewStream(outputStream.Context(), meta)
	logger = logger.WithField("stream_id", eventStream.ID())
	defer logger.Info("request finished")
	defer eventStream.Close()

	for {
		event, err := eventStream.Recv(ctx)
		if err != nil {
			return status.Error(codes.Canceled, "event stream cancelled")
		}

		err = outputStream.Send(event)
		if err != nil {
			return status.Error(codes.Internal, "failed to send event")
		}
	}
}

func (s *Server) SendMessage(ctx context.Context, req *pb.SendMessageRequest) (*pb.SendMessageResponse, error) {
	logger, err := s.verifyIdentity("SendMessage", req.Identity)
	if err != nil {
		return nil, err
	}
	defer logger.Info("request finished")

	err = s.client.WriteMessage(&irc.Message{
		Command: "PRIVMSG",
		Params:  []string{req.Target, req.Message},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.SendMessageResponse{}, nil
}

func (s *Server) SendRawMessage(ctx context.Context, req *pb.SendRawMessageRequest) (*pb.SendRawMessageResponse, error) {
	logger, err := s.verifyIdentity("SendRawMessage", req.Identity)
	if err != nil {
		return nil, err
	}
	defer logger.Info("request finished")

	err = s.client.WriteMessage(&irc.Message{
		Command: req.Command,
		Params:  req.Params,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.SendRawMessageResponse{}, nil
}

func (s *Server) JoinChannel(ctx context.Context, req *pb.JoinChannelRequest) (*pb.JoinChannelResponse, error) {
	logger, err := s.verifyIdentity("JoinChannel", req.Identity)
	if err != nil {
		return nil, err
	}
	defer logger.Info("request finished")

	// TODO: support channels which require a password to join
	// TODO: maybe it would make sense to wait until fully joined to a channel
	err = s.client.WriteMessage(&irc.Message{
		Command: "JOIN",
		Params:  []string{req.Name},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.JoinChannelResponse{}, nil
}

func (s *Server) LeaveChannel(ctx context.Context, req *pb.LeaveChannelRequest) (*pb.LeaveChannelResponse, error) {
	logger, err := s.verifyIdentity("LeaveChannel", req.Identity)
	if err != nil {
		return nil, err
	}
	defer logger.Info("request finished")

	// TODO: support leave messages
	err = s.client.WriteMessage(&irc.Message{
		Command: "PART",
		Params:  []string{req.Name},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.LeaveChannelResponse{}, nil
}

func (s *Server) ListChannels(ctx context.Context, req *pb.ListChannelsRequest) (*pb.ListChannelsResponse, error) {
	logger, err := s.verifyIdentity("ListChannels", req.Identity)
	if err != nil {
		return nil, err
	}
	defer logger.Info("request finished")

	return &pb.ListChannelsResponse{Names: s.tracker.ListChannels()}, nil
}

func (s *Server) GetChannelInfo(ctx context.Context, req *pb.ChannelInfoRequest) (*pb.ChannelInfoResponse, error) {
	logger, err := s.verifyIdentity("GetChannelInfo", req.Identity)
	if err != nil {
		return nil, err
	}
	defer logger.Info("request finished")

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

// SetChannelInfo implements SeabirdServer.SetChannelTopic
func (s *Server) SetChannelTopic(ctx context.Context, req *pb.SetChannelTopicRequest) (*pb.SetChannelTopicResponse, error) {
	logger, err := s.verifyIdentity("SetChannelTopic", req.Identity)
	if err != nil {
		return nil, err
	}
	defer logger.Info("request finished")

	channel := s.tracker.GetChannel(req.Name)
	if channel == nil {
		return nil, status.Error(codes.NotFound, "channel not found")
	}

	err = s.client.WriteMessage(&irc.Message{
		Command: "TOPIC",
		Params:  []string{req.Name, req.Topic},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.SetChannelTopicResponse{}, nil
}

// ListStreams implements SeabirdServer.ListStreams
func (s *Server) ListStreams(ctx context.Context, req *pb.ListStreamsRequest) (*pb.ListStreamsResponse, error) {
	logger, err := s.verifyIdentity("ListStreams", req.Identity)
	if err != nil {
		return nil, err
	}
	defer logger.Info("request finished")

	s.streamLock.RLock()
	defer s.streamLock.RUnlock()

	resp := &pb.ListStreamsResponse{}
	for id := range s.streams {
		resp.StreamIds = append(resp.StreamIds, id.String())
	}

	return resp, nil
}

// GetStreamInfo implements SeabirdServer.GetStreamInfo
func (s *Server) GetStreamInfo(ctx context.Context, req *pb.StreamInfoRequest) (*pb.StreamInfoResponse, error) {
	logger, err := s.verifyIdentity("GetStreamInfo", req.Identity)
	if err != nil {
		return nil, err
	}
	defer logger.Info("request finished")

	streamId, err := uuid.Parse(req.StreamId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid stream_id")
	}

	s.streamLock.RLock()
	defer s.streamLock.RUnlock()

	stream, ok := s.streams[streamId]
	if !ok {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	resp := &pb.StreamInfoResponse{
		Id:       stream.ID().String(),
		Tag:      stream.Tag(),
		Commands: make(map[string]*pb.CommandMetadata),
	}

	for _, command := range stream.Commands() {
		info := stream.CommandInfo(command)
		if info == nil {
			continue
		}

		resp.Commands[command] = &pb.CommandMetadata{
			Name:      info.Name,
			ShortHelp: info.ShortHelp,
			FullHelp:  info.FullHelp,
		}
	}

	return resp, nil
}
