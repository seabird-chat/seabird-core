package core

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/belak/x/slogx"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// IngestEvents is the bidirectional stream a chat backend holds open for its
// entire lifetime. The backend pushes chat events up and we push requests down.
func (s *Server) IngestEvents(stream pb.ChatIngest_IngestEventsServer) error {
	hello, err := recvHello(stream)
	if err != nil {
		return err
	}

	info := hello.GetBackendInfo()
	if info == nil {
		return status.Error(codes.InvalidArgument, "missing backend_info inner")
	}

	backendID := BackendID{Scheme: info.GetType(), ID: info.GetId()}

	backend, err := s.registerBackend(backendID)
	if err != nil {
		return err
	}
	defer s.unregisterBackend(backendID)

	logger := s.logger.With(slogx.String("backend", backendID.String()))
	logger.Info("backend connected")
	defer logger.Info("backend disconnected")

	// grpc-go only allows one goroutine to receive and one to send, so
	// incoming events are handled here and requests are sent from the loop
	// below. The receive goroutine unblocks on its own once this handler
	// returns and the stream is torn down.
	recvErr := make(chan error, 1)

	go func() {
		for {
			event, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}

			if err := s.handleChatEvent(backendID, backend, event); err != nil {
				recvErr <- err
				return
			}
		}
	}()

	for {
		select {
		case err := <-recvErr:
			if errors.Is(err, io.EOF) {
				return status.Error(codes.Internal, "chat event stream ended")
			}

			return err

		case req := <-backend.requests:
			if err := stream.Send(req); err != nil {
				return err
			}
		}
	}
}

// recvHello reads the mandatory first message of an ingest stream. The protocol
// requires Hello first so the backend can be identified before any of its
// events are accepted.
func recvHello(stream pb.ChatIngest_IngestEventsServer) (*pb.HelloChatEvent, error) {
	event, err := stream.Recv()
	if errors.Is(err, io.EOF) {
		return nil, status.Error(codes.InvalidArgument, "missing hello message")
	} else if err != nil {
		return nil, err
	}

	if event.Inner == nil {
		return nil, status.Error(codes.InvalidArgument, "missing hello message inner")
	}

	hello, ok := event.Inner.(*pb.ChatEvent_Hello)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "hello message inner is wrong type")
	}

	return hello.Hello, nil
}

// handleChatEvent answers any request the event was a response to, then either
// updates our view of the backend's channels or rebroadcasts the event to
// plugins.
func (s *Server) handleChatEvent(backendID BackendID, backend *chatBackend, event *pb.ChatEvent) error {
	if event.Inner == nil {
		return status.Error(codes.Internal, "missing inner event")
	}

	if event.Id != "" {
		s.respond(event.Id, event)
	}

	switch inner := event.Inner.(type) {
	// Success, Failed and Metadata only ever answer requests, which respond
	// above already took care of.
	case *pb.ChatEvent_Success, *pb.ChatEvent_Failed, *pb.ChatEvent_Metadata:

	case *pb.ChatEvent_Message:
		msg := inner.Message

		text, block, extraTags, err := normalizeBlock(msg.GetText(), msg.GetRootBlock())
		if err != nil {
			return err
		}

		s.broadcast(&pb.Event{
			Inner: &pb.Event_Message{Message: &pb.MessageEvent{
				Source:    relativeChannelSource(backendID, msg.GetSource()),
				Text:      text,
				RootBlock: block,
			}},
			Tags: mergeTags(event.Tags, extraTags),
		})

	case *pb.ChatEvent_PrivateMessage:
		msg := inner.PrivateMessage

		text, block, extraTags, err := normalizeBlock(msg.GetText(), msg.GetRootBlock())
		if err != nil {
			return err
		}

		s.broadcast(&pb.Event{
			Inner: &pb.Event_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
				Source:    relativeUser(backendID, msg.GetSource()),
				Text:      text,
				RootBlock: block,
			}},
			Tags: mergeTags(event.Tags, extraTags),
		})

	case *pb.ChatEvent_Mention:
		mention := inner.Mention

		text, block, extraTags, err := normalizeBlock(mention.GetText(), mention.GetRootBlock())
		if err != nil {
			return err
		}

		s.broadcast(&pb.Event{
			Inner: &pb.Event_Mention{Mention: &pb.MentionEvent{
				Source:    relativeChannelSource(backendID, mention.GetSource()),
				Text:      text,
				RootBlock: block,
			}},
			Tags: mergeTags(event.Tags, extraTags),
		})

	case *pb.ChatEvent_Action:
		action := inner.Action

		text, block, extraTags, err := normalizeBlock(action.GetText(), action.GetRootBlock())
		if err != nil {
			return err
		}

		s.broadcast(&pb.Event{
			Inner: &pb.Event_Action{Action: &pb.ActionEvent{
				Source:    relativeChannelSource(backendID, action.GetSource()),
				Text:      text,
				RootBlock: block,
			}},
			Tags: mergeTags(event.Tags, extraTags),
		})

	case *pb.ChatEvent_PrivateAction:
		action := inner.PrivateAction

		text, block, extraTags, err := normalizeBlock(action.GetText(), action.GetRootBlock())
		if err != nil {
			return err
		}

		s.broadcast(&pb.Event{
			Inner: &pb.Event_PrivateAction{PrivateAction: &pb.PrivateActionEvent{
				Source:    relativeUser(backendID, action.GetSource()),
				Text:      text,
				RootBlock: block,
			}},
			Tags: mergeTags(event.Tags, extraTags),
		})

	case *pb.ChatEvent_Command:
		cmd := inner.Command

		s.broadcast(&pb.Event{
			Inner: &pb.Event_Command{Command: &pb.CommandEvent{
				Source:  relativeChannelSource(backendID, cmd.GetSource()),
				Command: cmd.GetCommand(),
				Arg:     cmd.GetArg(),
			}},
			Tags: event.Tags,
		})

	case *pb.ChatEvent_JoinChannel:
		join := inner.JoinChannel

		backend.setChannel(&pb.Channel{
			Id:          join.GetChannelId(),
			DisplayName: join.GetDisplayName(),
			Topic:       join.GetTopic(),
		})

	case *pb.ChatEvent_LeaveChannel:
		backend.removeChannel(inner.LeaveChannel.GetChannelId())

	case *pb.ChatEvent_ChangeChannel:
		change := inner.ChangeChannel

		backend.updateChannel(change.GetChannelId(), change.GetDisplayName(), change.GetTopic())

	case *pb.ChatEvent_Hello:
		// A second hello means the backend broke the protocol contract, so
		// the connection gets killed.
		return status.Error(codes.InvalidArgument, "unexpected chat event type")

	default:
		// An event type we don't know about means the protos moved on without
		// us. Skipping it is better than dropping an otherwise healthy
		// backend's connection.
		s.logger.Warn("ignoring unknown chat event type",
			slogx.String("type", fmt.Sprintf("%T", event.Inner)))
	}

	return nil
}

// relativeUser rewrites a backend-local user ID into a fully qualified one.
func relativeUser(backendID BackendID, user *pb.User) *pb.User {
	if user == nil {
		return nil
	}

	return &pb.User{
		Id:          backendID.Relative(user.GetId()).String(),
		DisplayName: user.GetDisplayName(),
	}
}

// relativeChannelSource rewrites the backend-local IDs in a channel source into
// fully qualified ones.
func relativeChannelSource(backendID BackendID, source *pb.ChannelSource) *pb.ChannelSource {
	if source == nil {
		return nil
	}

	return &pb.ChannelSource{
		ChannelId: backendID.Relative(source.GetChannelId()).String(),
		User:      relativeUser(backendID, source.GetUser()),
	}
}

func (b *chatBackend) setChannel(channel *pb.Channel) {
	b.channelsMu.Lock()
	defer b.channelsMu.Unlock()

	b.channels[channel.GetId()] = channel
}

func (b *chatBackend) removeChannel(id string) {
	b.channelsMu.Lock()
	defer b.channelsMu.Unlock()

	delete(b.channels, id)
}

// updateChannel applies a change event. Changes for channels we don't know
// about are ignored, since we'd have no display name or topic to merge into.
func (b *chatBackend) updateChannel(id, displayName, topic string) {
	b.channelsMu.Lock()
	defer b.channelsMu.Unlock()

	if channel, ok := b.channels[id]; ok {
		channel.DisplayName = displayName
		channel.Topic = topic
	}
}

// channel returns a copy of a channel with its ID fully qualified.
func (b *chatBackend) channel(backendID BackendID, id string) (*pb.Channel, bool) {
	b.channelsMu.RLock()
	defer b.channelsMu.RUnlock()

	channel, ok := b.channels[id]
	if !ok {
		return nil, false
	}

	return qualifyChannel(backendID, channel), true
}

// channelList returns every known channel, ID-qualified and sorted by ID.
func (b *chatBackend) channelList(backendID BackendID) []*pb.Channel {
	b.channelsMu.RLock()
	defer b.channelsMu.RUnlock()

	ids := slices.Sorted(maps.Keys(b.channels))

	channels := make([]*pb.Channel, 0, len(ids))
	for _, id := range ids {
		channels = append(channels, qualifyChannel(backendID, b.channels[id]))
	}

	return channels
}

func qualifyChannel(backendID BackendID, channel *pb.Channel) *pb.Channel {
	return &pb.Channel{
		Id:          backendID.Relative(channel.GetId()).String(),
		DisplayName: channel.GetDisplayName(),
		Topic:       channel.GetTopic(),
	}
}
