package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/belak/x/slogx"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *Server) StreamEvents(
	req *pb.StreamEventsRequest, stream pb.Seabird_StreamEventsServer,
) error {
	ctx := stream.Context()

	// Registered all or none, and released however the stream ends.
	release, err := s.commands.register(req.GetCommands())
	if err != nil {
		return err
	}
	defer release()

	sub, err := s.Broker.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to events: %w", err)
	}
	defer sub.Close()

	logger := slogx.FromContext(ctx)
	logger.InfoContext(ctx, "plugin connected",
		slogx.Any("commands", slices.Sorted(maps.Keys(req.GetCommands()))))
	defer logger.InfoContext(ctx, "plugin disconnected")

	// Every plugin gets every event, and decides for itself which it cares about.
	for {
		event, err := sub.Next(ctx)
		if errors.Is(err, ErrLagged) {
			// A plugin that can't keep up is dropped rather than left to miss events
			// without knowing.
			logger.WarnContext(ctx, "plugin fell behind reading events, disconnecting")
			return status.Error(codes.Unavailable, "fell behind reading events")
		}
		if err != nil {
			return err
		}

		if err := stream.Send(event); err != nil {
			return fmt.Errorf("send event: %w", err)
		}
	}
}

func (s *Server) PerformAction(
	ctx context.Context, req *pb.PerformActionRequest,
) (*pb.PerformActionResponse, error) {
	username, err := authUsername(ctx)
	if err != nil {
		return nil, err
	}
	target, err := parseFullIDArg("channel_id", req.GetChannelId())
	if err != nil {
		return nil, err
	}
	block, err := normalizeBlock(req.GetText(), req.GetRootBlock())
	if err != nil {
		return nil, err
	}

	err = s.forward(ctx, target.Backend,
		&pb.Event{Tags: req.GetTags(), Inner: &pb.Event_PerformAction{PerformAction: &pb.PerformActionEvent{
			Sender:    username,
			ChannelId: req.GetChannelId(),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}},
		&pb.ChatRequest{Inner: &pb.ChatRequest_PerformAction{PerformAction: &pb.PerformActionChatRequest{
			ChannelId: target.Path,
			Text:      block.GetPlain(),
			RootBlock: block,
			Tags:      req.GetTags(),
		}}},
	)
	if err != nil {
		return nil, err
	}
	return &pb.PerformActionResponse{}, nil
}

func (s *Server) PerformPrivateAction(
	ctx context.Context, req *pb.PerformPrivateActionRequest,
) (*pb.PerformPrivateActionResponse, error) {
	username, err := authUsername(ctx)
	if err != nil {
		return nil, err
	}
	target, err := parseFullIDArg("user_id", req.GetUserId())
	if err != nil {
		return nil, err
	}
	block, err := normalizeBlock(req.GetText(), req.GetRootBlock())
	if err != nil {
		return nil, err
	}

	err = s.forward(ctx, target.Backend,
		&pb.Event{Tags: req.GetTags(), Inner: &pb.Event_PerformPrivateAction{PerformPrivateAction: &pb.PerformPrivateActionEvent{
			Sender:    username,
			UserId:    req.GetUserId(),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}},
		&pb.ChatRequest{Inner: &pb.ChatRequest_PerformPrivateAction{PerformPrivateAction: &pb.PerformPrivateActionChatRequest{
			UserId:    target.Path,
			Text:      block.GetPlain(),
			RootBlock: block,
			Tags:      req.GetTags(),
		}}},
	)
	if err != nil {
		return nil, err
	}
	return &pb.PerformPrivateActionResponse{}, nil
}

func (s *Server) SendMessage(
	ctx context.Context, req *pb.SendMessageRequest,
) (*pb.SendMessageResponse, error) {
	username, err := authUsername(ctx)
	if err != nil {
		return nil, err
	}
	target, err := parseFullIDArg("channel_id", req.GetChannelId())
	if err != nil {
		return nil, err
	}
	block, err := normalizeBlock(req.GetText(), req.GetRootBlock())
	if err != nil {
		return nil, err
	}

	err = s.forward(ctx, target.Backend,
		&pb.Event{Tags: req.GetTags(), Inner: &pb.Event_SendMessage{SendMessage: &pb.SendMessageEvent{
			Sender:    username,
			ChannelId: req.GetChannelId(),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}},
		&pb.ChatRequest{Inner: &pb.ChatRequest_SendMessage{SendMessage: &pb.SendMessageChatRequest{
			ChannelId: target.Path,
			Text:      block.GetPlain(),
			RootBlock: block,
			Tags:      req.GetTags(),
		}}},
	)
	if err != nil {
		return nil, err
	}
	return &pb.SendMessageResponse{}, nil
}

func (s *Server) SendPrivateMessage(
	ctx context.Context, req *pb.SendPrivateMessageRequest,
) (*pb.SendPrivateMessageResponse, error) {
	username, err := authUsername(ctx)
	if err != nil {
		return nil, err
	}
	target, err := parseFullIDArg("user_id", req.GetUserId())
	if err != nil {
		return nil, err
	}
	block, err := normalizeBlock(req.GetText(), req.GetRootBlock())
	if err != nil {
		return nil, err
	}

	err = s.forward(ctx, target.Backend,
		&pb.Event{Tags: req.GetTags(), Inner: &pb.Event_SendPrivateMessage{SendPrivateMessage: &pb.SendPrivateMessageEvent{
			Sender:    username,
			UserId:    req.GetUserId(),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}},
		&pb.ChatRequest{Inner: &pb.ChatRequest_SendPrivateMessage{SendPrivateMessage: &pb.SendPrivateMessageChatRequest{
			UserId:    target.Path,
			Text:      block.GetPlain(),
			RootBlock: block,
			Tags:      req.GetTags(),
		}}},
	)
	if err != nil {
		return nil, err
	}
	return &pb.SendPrivateMessageResponse{}, nil
}

func (s *Server) JoinChannel(
	ctx context.Context, req *pb.JoinChannelRequest,
) (*pb.JoinChannelResponse, error) {
	backend, err := parseBackendIDArg("backend_id", req.GetBackendId())
	if err != nil {
		return nil, err
	}

	reply, err := s.issueRequest(ctx, backend, &pb.ChatRequest{
		Inner: &pb.ChatRequest_JoinChannel{JoinChannel: &pb.JoinChannelChatRequest{
			ChannelName: req.GetChannelName(),
			Tags:        req.GetTags(),
		}},
	})
	if err != nil {
		return nil, err
	}

	switch reply.GetInner().(type) {
	case *pb.ChatEvent_Success, *pb.ChatEvent_JoinChannel:
		return &pb.JoinChannelResponse{}, nil
	default:
		return nil, replyError(reply)
	}
}

func (s *Server) LeaveChannel(
	ctx context.Context, req *pb.LeaveChannelRequest,
) (*pb.LeaveChannelResponse, error) {
	channel, err := parseFullIDArg("channel_id", req.GetChannelId())
	if err != nil {
		return nil, err
	}

	reply, err := s.issueRequest(ctx, channel.Backend, &pb.ChatRequest{
		Inner: &pb.ChatRequest_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatRequest{
			ChannelId: channel.Path,
			Tags:      req.GetTags(),
		}},
	})
	if err != nil {
		return nil, err
	}

	switch reply.GetInner().(type) {
	case *pb.ChatEvent_Success, *pb.ChatEvent_LeaveChannel:
		return &pb.LeaveChannelResponse{}, nil
	default:
		return nil, replyError(reply)
	}
}

func (s *Server) UpdateChannelInfo(
	ctx context.Context, req *pb.UpdateChannelInfoRequest,
) (*pb.UpdateChannelInfoResponse, error) {
	channel, err := parseFullIDArg("channel_id", req.GetChannelId())
	if err != nil {
		return nil, err
	}

	reply, err := s.issueRequest(ctx, channel.Backend, &pb.ChatRequest{
		Inner: &pb.ChatRequest_UpdateChannelInfo{UpdateChannelInfo: &pb.UpdateChannelInfoChatRequest{
			ChannelId: channel.Path,
			Topic:     req.GetTopic(),
			Tags:      req.GetTags(),
		}},
	})
	if err != nil {
		return nil, err
	}

	switch reply.GetInner().(type) {
	case *pb.ChatEvent_Success, *pb.ChatEvent_ChangeChannel:
		return &pb.UpdateChannelInfoResponse{}, nil
	default:
		return nil, replyError(reply)
	}
}

func (s *Server) ListBackends(
	ctx context.Context, req *pb.ListBackendsRequest,
) (*pb.ListBackendsResponse, error) {
	ids := s.backends.list()

	backends := make([]*pb.Backend, 0, len(ids))
	for _, id := range ids {
		backends = append(backends, &pb.Backend{Id: id.String(), Type: id.Type})
	}
	return &pb.ListBackendsResponse{Backends: backends}, nil
}

func (s *Server) GetBackendInfo(
	ctx context.Context, req *pb.BackendInfoRequest,
) (*pb.BackendInfoResponse, error) {
	id, err := parseBackendIDArg("backend_id", req.GetBackendId())
	if err != nil {
		return nil, err
	}

	reply, err := s.issueRequest(ctx, id, &pb.ChatRequest{
		Inner: &pb.ChatRequest_Metadata{Metadata: &pb.MetadataChatRequest{}},
	})
	if err != nil {
		return nil, err
	}

	switch inner := reply.GetInner().(type) {
	case *pb.ChatEvent_Metadata:
		return &pb.BackendInfoResponse{
			Backend:  &pb.Backend{Id: id.String(), Type: id.Type},
			Metadata: inner.Metadata.GetValues(),
		}, nil
	default:
		return nil, replyError(reply)
	}
}

func (s *Server) ListChannels(
	ctx context.Context, req *pb.ListChannelsRequest,
) (*pb.ListChannelsResponse, error) {
	id, err := parseBackendIDArg("backend_id", req.GetBackendId())
	if err != nil {
		return nil, err
	}
	b := s.backends.get(id)
	if b == nil {
		return nil, errBackendNotFound
	}

	known := b.channelList()
	channels := make([]*pb.Channel, 0, len(known))
	for _, channel := range known {
		channels = append(channels, relativeChannel(id, channel))
	}
	return &pb.ListChannelsResponse{Channels: channels}, nil
}

func (s *Server) GetChannelInfo(
	ctx context.Context, req *pb.ChannelInfoRequest,
) (*pb.ChannelInfoResponse, error) {
	id, err := parseFullIDArg("channel_id", req.GetChannelId())
	if err != nil {
		return nil, err
	}
	b := s.backends.get(id.Backend)
	if b == nil {
		return nil, errBackendNotFound
	}
	channel, ok := b.channel(id.Path)
	if !ok {
		return nil, status.Error(codes.NotFound, "channel not found")
	}

	return &pb.ChannelInfoResponse{Channel: relativeChannel(id.Backend, channel)}, nil
}

func (s *Server) GetCoreInfo(
	ctx context.Context, req *pb.CoreInfoRequest,
) (*pb.CoreInfoResponse, error) {
	return &pb.CoreInfoResponse{
		StartupTimestamp: uint64(s.startedAt.Unix()),
		CurrentTimestamp: uint64(time.Now().Unix()),
	}, nil
}

func (s *Server) RegisteredCommands(
	ctx context.Context, req *pb.CommandsRequest,
) (*pb.CommandsResponse, error) {
	return &pb.CommandsResponse{Commands: s.commands.list()}, nil
}

// forward publishes the event for a request a plugin made, then sends the
// request to the backend and waits for it to be carried out. The event is
// published every time, whether or not the request succeeds.
func (s *Server) forward(
	ctx context.Context, backend BackendID, event *pb.Event, request *pb.ChatRequest,
) error {
	if err := s.publish(ctx, event); err != nil {
		return err
	}

	reply, err := s.issueRequest(ctx, backend, request)
	if err != nil {
		return err
	}

	switch reply.GetInner().(type) {
	case *pb.ChatEvent_Success:
		return nil
	default:
		return replyError(reply)
	}
}

// replyError describes a backend's reply that wasn't what a request needed: its
// failure if it reported one, otherwise a protocol error.
func replyError(reply *pb.ChatEvent) error {
	if failed := reply.GetFailed(); failed != nil {
		return status.Error(codes.Unknown, failed.GetReason())
	}
	return status.Error(codes.Internal, "backend sent an unexpected reply")
}

// parseFullIDArg parses an ID from a plugin's request, naming the field if it
// is malformed.
func parseFullIDArg(field, value string) (FullID, error) {
	id, err := ParseFullID(value)
	if err != nil {
		return FullID{}, status.Errorf(codes.InvalidArgument, "invalid %s: %v", field, err)
	}
	return id, nil
}

// parseBackendIDArg parses a backend ID from a plugin's request, naming the
// field if it is malformed.
func parseBackendIDArg(field, value string) (BackendID, error) {
	id, err := ParseBackendID(value)
	if err != nil {
		return BackendID{}, status.Errorf(codes.InvalidArgument, "invalid %s: %v", field, err)
	}
	return id, nil
}

// relativeChannel describes a channel on a backend for a plugin, with an ID that
// includes the backend.
func relativeChannel(backend BackendID, channel channelInfo) *pb.Channel {
	return &pb.Channel{
		Id:          backend.Relative(channel.ID).String(),
		DisplayName: channel.DisplayName,
		Topic:       channel.Topic,
	}
}

// commandRegistry tracks the commands plugins have registered. Its zero value
// is ready to use.
type commandRegistry struct {
	mu       sync.Mutex
	commands map[string]*pb.CommandMetadata
}

// register adds commands, all or none: it fails if any is already registered.
// The returned function removes the commands again.
func (r *commandRegistry) register(commands map[string]*pb.CommandMetadata) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Sorted so that the command reported is always the same one.
	names := slices.Sorted(maps.Keys(commands))
	for _, name := range names {
		if _, taken := r.commands[name]; taken {
			return nil, status.Errorf(codes.AlreadyExists,
				"command %q already registered by another plugin", name)
		}
	}

	if r.commands == nil {
		r.commands = make(map[string]*pb.CommandMetadata)
	}
	for _, name := range names {
		r.commands[name] = proto.Clone(commands[name]).(*pb.CommandMetadata)
	}

	release := sync.OnceFunc(func() {
		r.mu.Lock()
		defer r.mu.Unlock()

		for _, name := range names {
			delete(r.commands, name)
		}
	})
	return release, nil
}

// list returns a copy of the registered commands.
func (r *commandRegistry) list() map[string]*pb.CommandMetadata {
	r.mu.Lock()
	defer r.mu.Unlock()

	commands := make(map[string]*pb.CommandMetadata, len(r.commands))
	for name, metadata := range r.commands {
		commands[name] = proto.Clone(metadata).(*pb.CommandMetadata)
	}
	return commands
}
