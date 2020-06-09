package seabird

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/seabird-irc/seabird-core/pb"
)

// Ensure that Server implements the SeabirdServer interface
var _ pb.SeabirdServer = (*Server)(nil)

func (s *Server) StreamEvents(req *pb.StreamEventsRequest, stream pb.Seabird_StreamEventsServer) error {
	ctx := stream.Context()
	logger := zerolog.Ctx(ctx)

	// TODO: handle commands

	streamId := uuid.New().String()
	logger.With().Str("stream_id", streamId)

	sub, ok := s.eventBox.Subscribe(streamId)
	if !ok {
		return status.Error(codes.Internal, "failed to get subscribe to event box")
	}
	defer sub.Close()

	for {
		event, ok := sub.Recv(ctx)
		if !ok {
			return status.Error(codes.Internal, "event stream ended")
		}

		var out *pb.Event

		switch v := event.(type) {
		case *pb.MessageEvent:
			out = &pb.Event{Inner: &pb.Event_Message{Message: v}}
		case *pb.PrivateMessageEvent:
			out = &pb.Event{Inner: &pb.Event_PrivateMessage{PrivateMessage: v}}
		case *pb.CommandEvent:
			out = &pb.Event{Inner: &pb.Event_Command{Command: v}}
		case *pb.MentionEvent:
			out = &pb.Event{Inner: &pb.Event_Mention{Mention: v}}
		default:
			// Unexpected event type
			continue
		}

		if out != nil {
			err := stream.Send(out)
			if err != nil {
				return err
			}
		}
	}
}

// Chat actions
func (s *Server) handleChatRequest(ctx context.Context, baseID string, req *pb.ChatRequest) (interface{}, error) {
	cs := s.LookupStream(baseID)
	if cs == nil {
		return nil, status.Error(codes.NotFound, "target not found")
	}

	req.Id = cs.RelativeID(uuid.New().String())

	sub, ok := s.requestResponseBox.Subscribe(req.Id)
	if !ok {
		return nil, status.Error(codes.Internal, "failed to get subscribe to response box")
	}
	defer sub.Close()

	if !cs.requestBoxHandle.Send(req) {
		return nil, status.Error(codes.Internal, "failed to send request")
	}

	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	resp, ok := sub.Recv(ctx)
	if !ok {
		return nil, status.Error(codes.DeadlineExceeded, "failed to get response")
	}

	return resp, nil
}

func (s *Server) SendMessage(ctx context.Context, req *pb.SendMessageRequest) (*pb.SendMessageResponse, error) {
	baseID, localID, ok := idParts(req.ChannelId)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "invalid channel ID")
	}

	resp, err := s.handleChatRequest(ctx, baseID, &pb.ChatRequest{Inner: &pb.ChatRequest_SendMessage{SendMessage: &pb.SendMessageChatRequest{
		ChannelId: localID,
		Text:      req.Text,
	}}})
	if err != nil {
		return nil, err
	}

	switch v := resp.(type) {
	case *pb.SuccessChatEvent:
		return &pb.SendMessageResponse{}, nil
	case *pb.FailedChatEvent:
		return nil, status.Error(codes.Aborted, v.Reason)
	default:
		return nil, status.Errorf(codes.Internal, "unexpected response type: %T", v)
	}
}
func (s *Server) SendPrivateMessage(ctx context.Context, req *pb.SendPrivateMessageRequest) (*pb.SendPrivateMessageResponse, error) {
	baseID, localID, ok := idParts(req.UserId)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "invalid channel ID")
	}

	resp, err := s.handleChatRequest(ctx, baseID, &pb.ChatRequest{Inner: &pb.ChatRequest_SendPrivateMessage{SendPrivateMessage: &pb.SendPrivateMessageChatRequest{
		UserId: localID,
		Text:   req.Text,
	}}})
	if err != nil {
		return nil, err
	}

	switch v := resp.(type) {
	case *pb.SuccessChatEvent:
		return &pb.SendPrivateMessageResponse{}, nil
	case *pb.FailedChatEvent:
		return nil, status.Error(codes.Aborted, v.Reason)
	default:
		return nil, status.Errorf(codes.Internal, "unexpected response type: %T", v)
	}

}
func (s *Server) JoinChannel(ctx context.Context, req *pb.JoinChannelRequest) (*pb.JoinChannelResponse, error) {
	resp, err := s.handleChatRequest(ctx, req.BackendId, &pb.ChatRequest{Inner: &pb.ChatRequest_JoinChannel{JoinChannel: &pb.JoinChannelChatRequest{
		ChannelName: req.ChannelName,
	}}})
	if err != nil {
		return nil, err
	}

	switch v := resp.(type) {
	case *pb.JoinChannelChatEvent:
		return &pb.JoinChannelResponse{}, nil
	case *pb.SuccessChatEvent:
		return &pb.JoinChannelResponse{}, nil
	case *pb.FailedChatEvent:
		return nil, status.Error(codes.Aborted, v.Reason)
	default:
		return nil, status.Errorf(codes.Internal, "unexpected response type: %T", v)
	}
}
func (s *Server) LeaveChannel(ctx context.Context, req *pb.LeaveChannelRequest) (*pb.LeaveChannelResponse, error) {
	// TODO: req.ExitMessage is unused

	baseID, localID, ok := idParts(req.ChannelId)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "invalid channel ID")
	}

	resp, err := s.handleChatRequest(ctx, baseID, &pb.ChatRequest{Inner: &pb.ChatRequest_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatRequest{
		ChannelId: localID,
	}}})
	if err != nil {
		return nil, err
	}

	switch v := resp.(type) {
	case *pb.LeaveChannelChatEvent:
		return &pb.LeaveChannelResponse{}, nil
	case *pb.SuccessChatEvent:
		return &pb.LeaveChannelResponse{}, nil
	case *pb.FailedChatEvent:
		return nil, status.Error(codes.Aborted, v.Reason)
	default:
		return nil, status.Errorf(codes.Internal, "unexpected response type: %T", v)
	}
}
func (s *Server) UpdateChannelInfo(ctx context.Context, req *pb.UpdateChannelInfoRequest) (*pb.UpdateChannelInfoResponse, error) {
	baseID, localID, ok := idParts(req.ChannelId)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "invalid channel ID")
	}

	resp, err := s.handleChatRequest(ctx, baseID, &pb.ChatRequest{Inner: &pb.ChatRequest_UpdateChannelInfo{UpdateChannelInfo: &pb.UpdateChannelInfoChatRequest{
		ChannelId: localID,
		Topic:     req.Topic,
	}}})
	if err != nil {
		return nil, err
	}

	switch v := resp.(type) {
	case *pb.UpdateChannelInfoChatRequest:
		return &pb.UpdateChannelInfoResponse{}, nil
	case *pb.SuccessChatEvent:
		return &pb.UpdateChannelInfoResponse{}, nil
	case *pb.FailedChatEvent:
		return nil, status.Error(codes.Aborted, v.Reason)
	default:
		return nil, status.Errorf(codes.Internal, "unexpected response type: %T", v)
	}
}

// Chat backend introspection
func (s *Server) ListBackends(ctx context.Context, req *pb.ListBackendsRequest) (*pb.ListBackendsResponse, error) {
	s.streamLock.RLock()
	defer s.streamLock.RUnlock()

	var backends []*pb.Backend

	for _, backend := range s.streams {
		backends = append(backends, &pb.Backend{Id: backend.GetID(), Type: backend.GetType()})
	}

	return &pb.ListBackendsResponse{
		Backends: backends,
	}, nil
}

func (s *Server) GetBackendInfo(ctx context.Context, req *pb.BackendInfoRequest) (*pb.BackendInfoResponse, error) {
	backend := s.LookupStream(req.BackendId)
	if backend == nil {
		return nil, status.Error(codes.NotFound, "backend not found")
	}

	return &pb.BackendInfoResponse{
		Backend: &pb.Backend{
			Id:   backend.GetID(),
			Type: backend.GetType(),
		},
	}, nil
}

// Chat connection introspection
func (s *Server) ListChannels(ctx context.Context, req *pb.ListChannelsRequest) (*pb.ListChannelsResponse, error) {
	backend := s.LookupStream(req.BackendId)
	if backend == nil {
		return nil, status.Error(codes.NotFound, "backend not found")
	}

	backend.channelLock.RLock()
	defer backend.channelLock.RUnlock()

	var channels []*pb.Channel

	for _, channel := range backend.channels {
		channels = append(channels, &pb.Channel{
			Id:          backend.RelativeID(channel.ID),
			DisplayName: channel.DisplayName,
			Topic:       channel.Topic,
		})
	}

	return &pb.ListChannelsResponse{
		Channels: channels,
	}, nil
}

func (s *Server) GetChannelInfo(ctx context.Context, req *pb.ChannelInfoRequest) (*pb.ChannelInfoResponse, error) {
	baseID, localID, ok := idParts(req.ChannelId)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "invalid channel ID")
	}

	backend := s.LookupStream(baseID)
	if backend == nil {
		return nil, status.Error(codes.NotFound, "backend not found")
	}

	channel := backend.LookupChannel(localID)
	if channel == nil {
		return nil, status.Error(codes.NotFound, "channel not found")
	}

	return &pb.ChannelInfoResponse{
		Channel: &pb.Channel{
			Id:          backend.RelativeID(channel.ID),
			DisplayName: channel.DisplayName,
			Topic:       channel.Topic,
		},
	}, nil
}

// Seabird introspection
func (s *Server) GetCoreInfo(ctx context.Context, req *pb.CoreInfoRequest) (*pb.CoreInfoResponse, error) {
	resp := &pb.CoreInfoResponse{
		StartupTimestamp: s.startTime.Unix(),
	}

	return resp, nil
}
