package core

import (
	"context"
	"time"

	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StreamEvents registers a plugin's commands and streams events to it until
// the client disconnects. The commands are released when the stream ends.
func (s *Server) StreamEvents(req *pb.StreamEventsRequest, stream pb.Seabird_StreamEventsServer) error {
	if err := s.registerCommands(req.GetCommands()); err != nil {
		return err
	}
	defer s.unregisterCommands(req.GetCommands())

	id, sub := s.subscribe()
	defer s.unsubscribe(id)

	ctx := stream.Context()

	for {
		select {
		case <-ctx.Done():
			// The client hung up, which is not an error.
			return nil
		case event, ok := <-sub.events:
			if !ok {
				return status.Error(codes.Internal, "client fell too far behind")
			}

			if err := stream.Send(event); err != nil {
				return err
			}
		}
	}
}

// outgoing is a relayed message after the target has been resolved and the
// block tree normalized.
type outgoing struct {
	sender string
	id     FullID
	text   string
	block  *pb.Block
	tags   map[string]string
}

// prepareOutgoing does the work shared by the four RPCs which relay text to a
// backend: identify the caller, resolve the target, and normalize the block.
// field names the request field holding target so errors can point at it.
func (s *Server) prepareOutgoing(
	ctx context.Context,
	field, target, text string,
	block *pb.Block,
	tags map[string]string,
) (*outgoing, error) {
	sender, err := authUsername(ctx)
	if err != nil {
		return nil, err
	}

	id, err := ParseFullID(target)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse %s", field)
	}

	text, block, extraTags, err := normalizeBlock(text, block)
	if err != nil {
		return nil, err
	}

	return &outgoing{
		sender: sender,
		id:     id,
		text:   text,
		block:  block,
		tags:   mergeTags(tags, extraTags),
	}, nil
}

func (s *Server) SendMessage(ctx context.Context, req *pb.SendMessageRequest) (*pb.SendMessageResponse, error) {
	out, err := s.prepareOutgoing(ctx, "channel_id",
		req.GetChannelId(), req.GetText(), req.GetRootBlock(), req.GetTags())
	if err != nil {
		return nil, err
	}

	s.broadcast(&pb.Event{
		Inner: &pb.Event_SendMessage{SendMessage: &pb.SendMessageEvent{
			Sender:    out.sender,
			ChannelId: req.GetChannelId(),
			Text:      out.text,
			RootBlock: out.block,
		}},
		Tags: out.tags,
	})

	response, err := s.issueRequest(ctx, out.id.Backend, &pb.ChatRequest{
		Inner: &pb.ChatRequest_SendMessage{SendMessage: &pb.SendMessageChatRequest{
			ChannelId: out.id.Path,
			Text:      out.text,
			RootBlock: out.block,
			Tags:      out.tags,
		}},
	})
	if err != nil {
		return nil, err
	}

	if _, ok := response.Inner.(*pb.ChatEvent_Success); !ok {
		return nil, chatError(response)
	}

	return &pb.SendMessageResponse{}, nil
}

func (s *Server) SendPrivateMessage(ctx context.Context, req *pb.SendPrivateMessageRequest) (*pb.SendPrivateMessageResponse, error) {
	out, err := s.prepareOutgoing(ctx, "user_id",
		req.GetUserId(), req.GetText(), req.GetRootBlock(), req.GetTags())
	if err != nil {
		return nil, err
	}

	s.broadcast(&pb.Event{
		Inner: &pb.Event_SendPrivateMessage{SendPrivateMessage: &pb.SendPrivateMessageEvent{
			Sender:    out.sender,
			UserId:    req.GetUserId(),
			Text:      out.text,
			RootBlock: out.block,
		}},
		Tags: out.tags,
	})

	response, err := s.issueRequest(ctx, out.id.Backend, &pb.ChatRequest{
		Inner: &pb.ChatRequest_SendPrivateMessage{SendPrivateMessage: &pb.SendPrivateMessageChatRequest{
			UserId:    out.id.Path,
			Text:      out.text,
			RootBlock: out.block,
			Tags:      out.tags,
		}},
	})
	if err != nil {
		return nil, err
	}

	if _, ok := response.Inner.(*pb.ChatEvent_Success); !ok {
		return nil, chatError(response)
	}

	return &pb.SendPrivateMessageResponse{}, nil
}

func (s *Server) PerformAction(ctx context.Context, req *pb.PerformActionRequest) (*pb.PerformActionResponse, error) {
	out, err := s.prepareOutgoing(ctx, "channel_id",
		req.GetChannelId(), req.GetText(), req.GetRootBlock(), req.GetTags())
	if err != nil {
		return nil, err
	}

	s.broadcast(&pb.Event{
		Inner: &pb.Event_PerformAction{PerformAction: &pb.PerformActionEvent{
			Sender:    out.sender,
			ChannelId: req.GetChannelId(),
			Text:      out.text,
			RootBlock: out.block,
		}},
		Tags: out.tags,
	})

	response, err := s.issueRequest(ctx, out.id.Backend, &pb.ChatRequest{
		Inner: &pb.ChatRequest_PerformAction{PerformAction: &pb.PerformActionChatRequest{
			ChannelId: out.id.Path,
			Text:      out.text,
			RootBlock: out.block,
			Tags:      out.tags,
		}},
	})
	if err != nil {
		return nil, err
	}

	if _, ok := response.Inner.(*pb.ChatEvent_Success); !ok {
		return nil, chatError(response)
	}

	return &pb.PerformActionResponse{}, nil
}

func (s *Server) PerformPrivateAction(ctx context.Context, req *pb.PerformPrivateActionRequest) (*pb.PerformPrivateActionResponse, error) {
	out, err := s.prepareOutgoing(ctx, "user_id",
		req.GetUserId(), req.GetText(), req.GetRootBlock(), req.GetTags())
	if err != nil {
		return nil, err
	}

	s.broadcast(&pb.Event{
		Inner: &pb.Event_PerformPrivateAction{PerformPrivateAction: &pb.PerformPrivateActionEvent{
			Sender:    out.sender,
			UserId:    req.GetUserId(),
			Text:      out.text,
			RootBlock: out.block,
		}},
		Tags: out.tags,
	})

	response, err := s.issueRequest(ctx, out.id.Backend, &pb.ChatRequest{
		Inner: &pb.ChatRequest_PerformPrivateAction{PerformPrivateAction: &pb.PerformPrivateActionChatRequest{
			UserId:    out.id.Path,
			Text:      out.text,
			RootBlock: out.block,
			Tags:      out.tags,
		}},
	})
	if err != nil {
		return nil, err
	}

	if _, ok := response.Inner.(*pb.ChatEvent_Success); !ok {
		return nil, chatError(response)
	}

	return &pb.PerformPrivateActionResponse{}, nil
}

func (s *Server) JoinChannel(ctx context.Context, req *pb.JoinChannelRequest) (*pb.JoinChannelResponse, error) {
	backendID, err := ParseBackendID(req.GetBackendId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "failed to parse backend_id")
	}

	response, err := s.issueRequest(ctx, backendID, &pb.ChatRequest{
		Inner: &pb.ChatRequest_JoinChannel{JoinChannel: &pb.JoinChannelChatRequest{
			ChannelName: req.GetChannelName(),
			Tags:        req.GetTags(),
		}},
	})
	if err != nil {
		return nil, err
	}

	switch response.Inner.(type) {
	case *pb.ChatEvent_Success, *pb.ChatEvent_JoinChannel:
		return &pb.JoinChannelResponse{}, nil
	default:
		return nil, chatError(response)
	}
}

func (s *Server) LeaveChannel(ctx context.Context, req *pb.LeaveChannelRequest) (*pb.LeaveChannelResponse, error) {
	id, err := ParseFullID(req.GetChannelId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "failed to parse channel_id")
	}

	response, err := s.issueRequest(ctx, id.Backend, &pb.ChatRequest{
		Inner: &pb.ChatRequest_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatRequest{
			ChannelId: id.Path,
			Tags:      req.GetTags(),
		}},
	})
	if err != nil {
		return nil, err
	}

	switch response.Inner.(type) {
	case *pb.ChatEvent_Success, *pb.ChatEvent_LeaveChannel:
		return &pb.LeaveChannelResponse{}, nil
	default:
		return nil, chatError(response)
	}
}

func (s *Server) UpdateChannelInfo(ctx context.Context, req *pb.UpdateChannelInfoRequest) (*pb.UpdateChannelInfoResponse, error) {
	id, err := ParseFullID(req.GetChannelId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "failed to parse channel_id")
	}

	response, err := s.issueRequest(ctx, id.Backend, &pb.ChatRequest{
		Inner: &pb.ChatRequest_UpdateChannelInfo{UpdateChannelInfo: &pb.UpdateChannelInfoChatRequest{
			ChannelId: id.Path,
			Topic:     req.GetTopic(),
			Tags:      req.GetTags(),
		}},
	})
	if err != nil {
		return nil, err
	}

	switch response.Inner.(type) {
	case *pb.ChatEvent_Success, *pb.ChatEvent_ChangeChannel:
		return &pb.UpdateChannelInfoResponse{}, nil
	default:
		return nil, chatError(response)
	}
}

func (s *Server) ListBackends(ctx context.Context, req *pb.ListBackendsRequest) (*pb.ListBackendsResponse, error) {
	ids := s.backendIDs()

	backends := make([]*pb.Backend, 0, len(ids))
	for _, id := range ids {
		backends = append(backends, &pb.Backend{Id: id.String(), Type: id.Scheme})
	}

	return &pb.ListBackendsResponse{Backends: backends}, nil
}

func (s *Server) GetBackendInfo(ctx context.Context, req *pb.BackendInfoRequest) (*pb.BackendInfoResponse, error) {
	backendID, err := ParseBackendID(req.GetBackendId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "failed to parse backend_id")
	}

	if _, ok := s.lookupBackend(backendID); !ok {
		return nil, status.Error(codes.NotFound, "backend not found")
	}

	response, err := s.issueRequest(ctx, backendID, &pb.ChatRequest{
		Inner: &pb.ChatRequest_Metadata{Metadata: &pb.MetadataChatRequest{}},
	})
	if err != nil {
		return nil, err
	}

	metadata, ok := response.Inner.(*pb.ChatEvent_Metadata)
	if !ok {
		return nil, chatError(response)
	}

	return &pb.BackendInfoResponse{
		Backend:  &pb.Backend{Id: backendID.String(), Type: backendID.Scheme},
		Metadata: metadata.Metadata.GetValues(),
	}, nil
}

func (s *Server) ListChannels(ctx context.Context, req *pb.ListChannelsRequest) (*pb.ListChannelsResponse, error) {
	backendID, err := ParseBackendID(req.GetBackendId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "failed to parse backend_id")
	}

	backend, ok := s.lookupBackend(backendID)
	if !ok {
		return nil, status.Error(codes.NotFound, "backend not found")
	}

	return &pb.ListChannelsResponse{Channels: backend.channelList(backendID)}, nil
}

func (s *Server) GetChannelInfo(ctx context.Context, req *pb.ChannelInfoRequest) (*pb.ChannelInfoResponse, error) {
	id, err := ParseFullID(req.GetChannelId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "failed to parse channel_id")
	}

	backend, ok := s.lookupBackend(id.Backend)
	if !ok {
		return nil, status.Error(codes.NotFound, "backend not found")
	}

	channel, ok := backend.channel(id.Backend, id.Path)
	if !ok {
		return nil, status.Error(codes.NotFound, "channel not found")
	}

	return &pb.ChannelInfoResponse{Channel: channel}, nil
}

func (s *Server) GetCoreInfo(ctx context.Context, req *pb.CoreInfoRequest) (*pb.CoreInfoResponse, error) {
	return &pb.CoreInfoResponse{
		StartupTimestamp: s.startupTimestamp,
		CurrentTimestamp: uint64(time.Now().Unix()),
	}, nil
}

func (s *Server) RegisteredCommands(ctx context.Context, req *pb.CommandsRequest) (*pb.CommandsResponse, error) {
	s.commandsMu.RLock()
	defer s.commandsMu.RUnlock()

	commands := make(map[string]*pb.CommandMetadata, len(s.commands))
	for name, metadata := range s.commands {
		commands[name] = metadata
	}

	return &pb.CommandsResponse{Commands: commands}, nil
}
