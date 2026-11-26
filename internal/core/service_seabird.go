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
	"github.com/seabird-chat/seabird-core/internal/pb"
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
	if err := checkChannelID("channel_id", req.GetChannelId()); err != nil {
		return nil, err
	}
	block, err := normalizeBlock(req.GetText(), req.GetRootBlock())
	if err != nil {
		return nil, err
	}

	err = s.forwardToChannel(ctx, req.GetChannelId(),
		func(channelID string) *pb.Event {
			return &pb.Event{Tags: req.GetTags(), Inner: &pb.Event_PerformAction{PerformAction: &pb.PerformActionEvent{
				Sender:    username,
				ChannelId: channelID,
				Text:      block.GetPlain(),
				RootBlock: block,
			}}}
		},
		func(member Channel) *pb.ChatRequest {
			return &pb.ChatRequest{Inner: &pb.ChatRequest_PerformAction{PerformAction: &pb.PerformActionChatRequest{
				ChannelId: member.ExternalID,
				Text:      block.GetPlain(),
				RootBlock: block,
				Tags:      req.GetTags(),
			}}}
		},
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
	if err := checkID("user_id", req.GetUserId(), userIDPrefix); err != nil {
		return nil, err
	}
	block, err := normalizeBlock(req.GetText(), req.GetRootBlock())
	if err != nil {
		return nil, err
	}

	err = s.forward(ctx,
		&pb.Event{Tags: req.GetTags(), Inner: &pb.Event_PerformPrivateAction{PerformPrivateAction: &pb.PerformPrivateActionEvent{
			Sender:    username,
			UserId:    req.GetUserId(),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}},
		func() (*backend, *pb.ChatRequest, error) {
			target, b, err := s.userTarget(ctx, "user_id", req.GetUserId())
			if err != nil {
				return nil, nil, err
			}
			return b, &pb.ChatRequest{Inner: &pb.ChatRequest_PerformPrivateAction{PerformPrivateAction: &pb.PerformPrivateActionChatRequest{
				UserId:    target.ExternalID,
				Text:      block.GetPlain(),
				RootBlock: block,
				Tags:      req.GetTags(),
			}}}, nil
		},
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
	if err := checkChannelID("channel_id", req.GetChannelId()); err != nil {
		return nil, err
	}
	block, err := normalizeBlock(req.GetText(), req.GetRootBlock())
	if err != nil {
		return nil, err
	}

	err = s.forwardToChannel(ctx, req.GetChannelId(),
		func(channelID string) *pb.Event {
			return &pb.Event{Tags: req.GetTags(), Inner: &pb.Event_SendMessage{SendMessage: &pb.SendMessageEvent{
				Sender:    username,
				ChannelId: channelID,
				Text:      block.GetPlain(),
				RootBlock: block,
			}}}
		},
		func(member Channel) *pb.ChatRequest {
			return &pb.ChatRequest{Inner: &pb.ChatRequest_SendMessage{SendMessage: &pb.SendMessageChatRequest{
				ChannelId: member.ExternalID,
				Text:      block.GetPlain(),
				RootBlock: block,
				Tags:      req.GetTags(),
			}}}
		},
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
	if err := checkID("user_id", req.GetUserId(), userIDPrefix); err != nil {
		return nil, err
	}
	block, err := normalizeBlock(req.GetText(), req.GetRootBlock())
	if err != nil {
		return nil, err
	}

	err = s.forward(ctx,
		&pb.Event{Tags: req.GetTags(), Inner: &pb.Event_SendPrivateMessage{SendPrivateMessage: &pb.SendPrivateMessageEvent{
			Sender:    username,
			UserId:    req.GetUserId(),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}},
		func() (*backend, *pb.ChatRequest, error) {
			target, b, err := s.userTarget(ctx, "user_id", req.GetUserId())
			if err != nil {
				return nil, nil, err
			}
			return b, &pb.ChatRequest{Inner: &pb.ChatRequest_SendPrivateMessage{SendPrivateMessage: &pb.SendPrivateMessageChatRequest{
				UserId:    target.ExternalID,
				Text:      block.GetPlain(),
				RootBlock: block,
				Tags:      req.GetTags(),
			}}}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return &pb.SendPrivateMessageResponse{}, nil
}

func (s *Server) JoinChannel(
	ctx context.Context, req *pb.JoinChannelRequest,
) (*pb.JoinChannelResponse, error) {
	_, b, err := s.backendTarget(ctx, "backend_id", req.GetBackendId())
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errBackendNotConnected
	}

	reply, err := s.issueRequest(ctx, b, &pb.ChatRequest{
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
	channel, b, err := s.channelTarget(ctx, "channel_id", req.GetChannelId())
	if err != nil {
		return nil, err
	}

	reply, err := s.issueRequest(ctx, b, &pb.ChatRequest{
		Inner: &pb.ChatRequest_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatRequest{
			ChannelId: channel.ExternalID,
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
	channel, b, err := s.channelTarget(ctx, "channel_id", req.GetChannelId())
	if err != nil {
		return nil, err
	}

	reply, err := s.issueRequest(ctx, b, &pb.ChatRequest{
		Inner: &pb.ChatRequest_UpdateChannelInfo{UpdateChannelInfo: &pb.UpdateChannelInfoChatRequest{
			ChannelId: channel.ExternalID,
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
	rows, err := s.DB.ListBackends(ctx, req.GetIncludeDisconnected())
	if err != nil {
		return nil, err
	}

	backends := make([]*pb.Backend, 0, len(rows))
	for _, row := range rows {
		backends = append(backends, backendProto(row))
	}
	return &pb.ListBackendsResponse{Backends: backends}, nil
}

func (s *Server) GetBackendInfo(
	ctx context.Context, req *pb.BackendInfoRequest,
) (*pb.BackendInfoResponse, error) {
	row, b, err := s.backendTarget(ctx, "backend_id", req.GetBackendId())
	if err != nil {
		return nil, err
	}

	// Only a connected backend can be asked for its metadata.
	if b == nil {
		return &pb.BackendInfoResponse{Backend: backendProto(*row)}, nil
	}

	reply, err := s.issueRequest(ctx, b, &pb.ChatRequest{
		Inner: &pb.ChatRequest_Metadata{Metadata: &pb.MetadataChatRequest{}},
	})
	if err != nil {
		return nil, err
	}

	switch inner := reply.GetInner().(type) {
	case *pb.ChatEvent_Metadata:
		return &pb.BackendInfoResponse{
			Backend:  backendProto(*row),
			Metadata: inner.Metadata.GetValues(),
		}, nil
	default:
		return nil, replyError(reply)
	}
}

func (s *Server) ListChannels(
	ctx context.Context, req *pb.ListChannelsRequest,
) (*pb.ListChannelsResponse, error) {
	// An empty backend ID lists every backend's channels.
	backendID := req.GetBackendId()
	if backendID != "" {
		if _, _, err := s.backendTarget(ctx, "backend_id", backendID); err != nil {
			return nil, err
		}
	}

	rows, err := s.listChannels(ctx, backendID, req.GetIncludeInactive())
	if err != nil {
		return nil, err
	}

	channels := make([]*pb.Channel, 0, len(rows))
	for _, row := range rows {
		channels = append(channels, s.channelProto(row))
	}
	return &pb.ListChannelsResponse{Channels: channels}, nil
}

func (s *Server) GetChannelInfo(
	ctx context.Context, req *pb.ChannelInfoRequest,
) (*pb.ChannelInfoResponse, error) {
	channel, err := s.resolveChannel(ctx, "channel_id", req.GetChannelId())
	if err != nil {
		return nil, err
	}
	return &pb.ChannelInfoResponse{Channel: s.channelProto(channel)}, nil
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

// forward publishes the event for a request a plugin made, then looks up who to
// send it to, sends it, and waits for it to be carried out. The event is
// published every time, whether or not the target can be reached or the request
// succeeds.
func (s *Server) forward(
	ctx context.Context, event *pb.Event, request func() (*backend, *pb.ChatRequest, error),
) error {
	if err := s.publish(ctx, event); err != nil {
		return err
	}

	b, chatRequest, err := request()
	if err != nil {
		return err
	}
	reply, err := s.issueRequest(ctx, b, chatRequest)
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

// forwardToChannel publishes the event for a request a plugin made to a
// channel, then sends a request to every member at once. It succeeds if any
// member carries it out, and otherwise returns the first failure. A group's
// failures are logged. The event names the channel as plugins know it, or as the request
// did if it can't be found, and is published whatever happens.
func (s *Server) forwardToChannel(
	ctx context.Context, id string, event func(channelID string) *pb.Event, request func(member Channel) *pb.ChatRequest,
) error {
	channel, resolveErr := s.resolveChannel(ctx, "channel_id", id)
	if resolveErr == nil {
		id = channel.id
	}
	if err := s.publish(ctx, event(id)); err != nil {
		return err
	}
	if resolveErr != nil {
		return resolveErr
	}
	if len(channel.members) == 0 {
		return errGroupEmpty
	}

	errs := make([]error, len(channel.members))
	var wg sync.WaitGroup
	for i, member := range channel.members {
		wg.Go(func() {
			errs[i] = s.sendToMember(ctx, member, request(member))
		})
	}
	wg.Wait()

	// A channel's only failure is returned below, so only groups log.
	for i, err := range errs {
		if err != nil && channel.group {
			slogx.FromContext(ctx).WarnContext(ctx, "failed to send to channel group member",
				slogx.String("channel_id", channel.members[i].ID), slogx.Err(err))
		}
	}
	if !slices.Contains(errs, nil) {
		return errs[0]
	}
	return nil
}

var errGroupEmpty = status.Error(codes.FailedPrecondition, "channel group has no members")

// sendToMember sends request to a backend channel's backend, and waits for it
// to be carried out.
func (s *Server) sendToMember(ctx context.Context, member Channel, request *pb.ChatRequest) error {
	b := s.backends.get(member.BackendID)
	if b == nil {
		return errBackendNotConnected
	}
	reply, err := s.issueRequest(ctx, b, request)
	if err != nil {
		return err
	}
	if _, ok := reply.GetInner().(*pb.ChatEvent_Success); !ok {
		return replyError(reply)
	}
	return nil
}

// replyError describes a backend's reply that wasn't what a request needed: its
// failure if it reported one, otherwise a protocol error.
func replyError(reply *pb.ChatEvent) error {
	if failed := reply.GetFailed(); failed != nil {
		return status.Error(codes.Unknown, failed.GetReason())
	}
	return status.Error(codes.Internal, "backend sent an unexpected reply")
}

var (
	errBackendNotFound = status.Error(codes.NotFound, "backend not found")
	errChannelNotFound = status.Error(codes.NotFound, "channel not found")
	errUserNotFound    = status.Error(codes.NotFound, "user not found")
)

// backendTarget looks up the backend a plugin's request names in field, and
// returns it with its connection, which is nil if it isn't connected.
func (s *Server) backendTarget(ctx context.Context, field, id string) (*Backend, *backend, error) {
	if err := checkID(field, id, backendIDPrefix); err != nil {
		return nil, nil, err
	}
	row, err := s.DB.GetBackend(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, errBackendNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return row, s.backends.get(row.ID), nil
}

var errGroupUnsupported = status.Error(codes.FailedPrecondition, "not supported on channel groups")

// channelTarget looks up the channel a plugin's request names in field, and the
// connected backend it is on. Groups are refused, since they span backends.
func (s *Server) channelTarget(ctx context.Context, field, id string) (*Channel, *backend, error) {
	resolved, err := s.resolveChannel(ctx, field, id)
	if err != nil {
		return nil, nil, err
	}
	if resolved.group {
		return nil, nil, errGroupUnsupported
	}
	channel := resolved.members[0]

	b := s.backends.get(channel.BackendID)
	if b == nil {
		return nil, nil, errBackendNotConnected
	}
	return &channel, b, nil
}

// userTarget looks up the user a plugin's request names in field, and the
// connected backend they are on.
func (s *Server) userTarget(ctx context.Context, field, id string) (*User, *backend, error) {
	if err := checkID(field, id, userIDPrefix); err != nil {
		return nil, nil, err
	}
	user, err := s.DB.GetUser(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, errUserNotFound
	}
	if err != nil {
		return nil, nil, err
	}

	b := s.backends.get(user.BackendID)
	if b == nil {
		return nil, nil, errBackendNotConnected
	}
	return user, b, nil
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
