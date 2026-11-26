package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/belak/x/slogx"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// received is one result of reading a backend's stream.
type received struct {
	event *pb.ChatEvent
	err   error
}

// IngestEvents is the stream a chat backend keeps open to core. The backend
// introduces itself, then sends events and replies to requests while core sends
// it the requests plugins make.
func (s *Server) IngestEvents(stream pb.ChatIngest_IngestEventsServer) (err error) {
	ctx := stream.Context()

	// One goroutine reads the stream, one handles events, and this one sends
	// requests. Handling is separate so a Send blocked on a backend that isn't
	// reading can't hold up its replies.
	recv := make(chan received)
	go readStream(ctx, stream, recv)

	key, err := receiveHello(ctx, recv, s.helloTimeout())
	if err != nil {
		return err
	}

	// The key is released last, so a backend reconnecting under the same name
	// can't connect until this stream has finished disconnecting.
	b, err := s.backends.register(key)
	if err != nil {
		return err
	}
	defer s.backends.remove(b)

	logger := slogx.FromContext(ctx).With(
		slogx.String("backend_type", key.Type),
		slogx.String("backend_name", key.Name),
	)
	ctx = slogx.WithLogger(ctx, logger)

	row, err := s.DB.ConnectBackend(ctx, key.Type, key.Name)
	if err != nil {
		return fmt.Errorf("record backend: %w", err)
	}
	s.backends.activate(b, row)
	defer s.disconnectBackend(ctx, b)

	logger.InfoContext(ctx, "backend connected", slogx.String("backend_id", row.ID))
	defer func() {
		if err != nil {
			logger.InfoContext(ctx, "backend disconnected", slogx.Err(err))
		} else {
			logger.InfoContext(ctx, "backend disconnected")
		}
	}()

	if err := s.publish(ctx, backendUpdatedEvent(*row)); err != nil {
		return err
	}

	// This goroutine ends when the stream does, or with the RPC.
	handled := make(chan error, 1)
	go func() {
		handled <- s.receiveEvents(ctx, b, recv)
	}()

	for {
		select {
		case request := <-b.requests:
			if err := stream.Send(request); err != nil {
				// A failed Send aborts the stream, which ends the handler too.
				<-handled
				return fmt.Errorf("send request to backend: %w", err)
			}

		case err := <-handled:
			return err
		}
	}
}

// readStream passes everything the backend sends to out, then the error that
// ended the stream. It stops when ctx is done, possibly without reporting, so
// whoever reads out must watch ctx too.
func readStream(ctx context.Context, stream pb.ChatIngest_IngestEventsServer, out chan<- received) {
	for {
		event, err := stream.Recv()

		select {
		case out <- received{event, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

var errBadHello = status.Error(codes.InvalidArgument, "first message must be a hello with a backend type and id")

// receiveHello waits up to timeout for the backend's first event, which must
// identify it.
func receiveHello(
	ctx context.Context, recv <-chan received, timeout time.Duration,
) (backendKey, error) {
	var event *pb.ChatEvent

	select {
	case got := <-recv:
		if errors.Is(got.err, io.EOF) {
			return backendKey{}, errBadHello
		}
		if got.err != nil {
			return backendKey{}, got.err
		}
		event = got.event
	case <-time.After(timeout):
		return backendKey{}, status.Error(codes.DeadlineExceeded, "timed out waiting for hello message")
	case <-ctx.Done():
		return backendKey{}, ctx.Err()
	}

	// The getters return zero values for anything missing, so one check covers
	// a missing hello, backend info, type or ID.
	info := event.GetHello().GetBackendInfo()
	if info.GetType() == "" || info.GetId() == "" {
		return backendKey{}, errBadHello
	}
	return backendKey{Type: info.GetType(), Name: info.GetId()}, nil
}

// receiveEvents handles the backend's events until the stream ends. It returns
// nil if the backend closes the stream cleanly.
func (s *Server) receiveEvents(ctx context.Context, b *backend, recv <-chan received) error {
	for {
		select {
		case got := <-recv:
			if errors.Is(got.err, io.EOF) {
				return nil
			}
			if got.err != nil {
				return got.err
			}
			if err := s.handleChatEvent(ctx, b, got.event); err != nil {
				return err
			}

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// handleChatEvent handles an event from a backend. An error ends the stream.
//
// Replies to the backend's requests arrive through here, so it must not wait
// for a request.
func (s *Server) handleChatEvent(ctx context.Context, b *backend, event *pb.ChatEvent) error {
	switch inner := event.GetInner().(type) {
	case nil:
		return status.Error(codes.InvalidArgument, "missing inner event")

	// The backend has already introduced itself.
	case *pb.ChatEvent_Hello:
		return status.Error(codes.InvalidArgument, "unexpected chat event type")

	// Replies go to the caller waiting for them, if there still is one.
	case *pb.ChatEvent_Success, *pb.ChatEvent_Failed, *pb.ChatEvent_Metadata:
		deliverReply(ctx, b, event)
		return nil

	// Channel changes update what core knows. A request to join, leave or update
	// a channel can be answered by the matching change.
	case *pb.ChatEvent_JoinChannel:
		join := inner.JoinChannel
		channel, changed, err := s.DB.JoinChannel(ctx, b.row.ID, join.GetChannelId(), join.GetDisplayName(), join.GetTopic())
		if err != nil {
			return fmt.Errorf("record join: %w", err)
		}
		if err := s.publishChannel(ctx, channel, changed); err != nil {
			return err
		}
		deliverReply(ctx, b, event)
		return nil
	case *pb.ChatEvent_LeaveChannel:
		channel, changed, err := s.DB.LeaveChannel(ctx, b.row.ID, inner.LeaveChannel.GetChannelId())
		if err != nil {
			return fmt.Errorf("record leave: %w", err)
		}
		if err := s.publishChannel(ctx, channel, changed); err != nil {
			return err
		}
		deliverReply(ctx, b, event)
		return nil
	case *pb.ChatEvent_ChangeChannel:
		change := inner.ChangeChannel
		channel, changed, err := s.DB.ChangeChannel(ctx, b.row.ID, change.GetChannelId(), change.GetDisplayName(), change.GetTopic())
		if err != nil {
			return fmt.Errorf("record change: %w", err)
		}
		if err := s.publishChannel(ctx, channel, changed); err != nil {
			return err
		}
		deliverReply(ctx, b, event)
		return nil

	// Chat becomes events for plugins, with the IDs plugins use and with the
	// text and blocks made consistent.
	case *pb.ChatEvent_Message:
		message := inner.Message
		source, err := s.resolveSource(ctx, b, message.GetSource())
		if err != nil {
			return err
		}
		block, err := normalizeBlock(message.GetText(), message.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_Message{Message: &pb.MessageEvent{
			Source:    source,
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})
	case *pb.ChatEvent_PrivateMessage:
		message := inner.PrivateMessage
		source, err := s.resolveUser(ctx, b, message.GetSource())
		if err != nil {
			return err
		}
		block, err := normalizeBlock(message.GetText(), message.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
			Source:    source,
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})
	case *pb.ChatEvent_Mention:
		mention := inner.Mention
		source, err := s.resolveSource(ctx, b, mention.GetSource())
		if err != nil {
			return err
		}
		block, err := normalizeBlock(mention.GetText(), mention.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_Mention{Mention: &pb.MentionEvent{
			Source:    source,
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})
	case *pb.ChatEvent_Action:
		action := inner.Action
		source, err := s.resolveSource(ctx, b, action.GetSource())
		if err != nil {
			return err
		}
		block, err := normalizeBlock(action.GetText(), action.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_Action{Action: &pb.ActionEvent{
			Source:    source,
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})
	case *pb.ChatEvent_PrivateAction:
		action := inner.PrivateAction
		source, err := s.resolveUser(ctx, b, action.GetSource())
		if err != nil {
			return err
		}
		block, err := normalizeBlock(action.GetText(), action.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_PrivateAction{PrivateAction: &pb.PrivateActionEvent{
			Source:    source,
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})

	// Commands are already split up, so there is no text to normalize.
	case *pb.ChatEvent_Command:
		command := inner.Command
		source, err := s.resolveSource(ctx, b, command.GetSource())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_Command{Command: &pb.CommandEvent{
			Source:  source,
			Command: command.GetCommand(),
			Arg:     command.GetArg(),
		}}})

	default:
		return status.Errorf(codes.Unimplemented, "chat event %T not implemented", inner)
	}
}

// deliverReply gives event to the caller waiting for the request it answers.
// Events with no request ID aren't replies to anything.
func deliverReply(ctx context.Context, b *backend, event *pb.ChatEvent) {
	id := event.GetId()
	if id == "" || b.respond(id, event) {
		return
	}

	slogx.FromContext(ctx).DebugContext(ctx, "ignoring reply to unknown request",
		slogx.String("request_id", id))
}

// publish sends an event to the plugins.
func (s *Server) publish(ctx context.Context, event *pb.Event) error {
	if err := s.Broker.Publish(ctx, event); err != nil {
		return fmt.Errorf("publish event: %w", err)
	}
	return nil
}

// disconnectBackend marks b as disconnected and tells plugins, including about
// each channel it was in.
func (s *Server) disconnectBackend(ctx context.Context, b *backend) {
	s.backends.deactivate(b)

	// The stream has ended by now, but cleaning up still has to happen.
	ctx = context.WithoutCancel(ctx)
	logger := slogx.FromContext(ctx)

	left, err := s.DB.DisconnectBackend(ctx, b.row.ID)
	if err != nil {
		logger.WarnContext(ctx, "failed to record backend disconnecting", slogx.Err(err))
		return
	}

	row := *b.row
	row.Connected = false
	if err := s.publish(ctx, backendUpdatedEvent(row)); err != nil {
		logger.WarnContext(ctx, "failed to announce backend disconnecting", slogx.Err(err))
	}
	for _, channel := range left {
		if err := s.publish(ctx, channelUpdatedEvent(channel, false)); err != nil {
			logger.WarnContext(ctx, "failed to announce channel leaving", slogx.Err(err))
		}
	}
}

// publishChannel tells plugins about a channel on a connected backend, if it
// changed.
func (s *Server) publishChannel(ctx context.Context, channel *Channel, changed bool) error {
	if !changed {
		return nil
	}
	return s.publish(ctx, channelUpdatedEvent(*channel, true))
}

// resolveSource returns a source from a backend with the IDs plugins use,
// recording its channel and user if they're new.
func (s *Server) resolveSource(ctx context.Context, b *backend, source *pb.ChannelSource) (*pb.ChannelSource, error) {
	if source == nil {
		return nil, nil
	}

	user, err := s.resolveUser(ctx, b, source.GetUser())
	if err != nil {
		return nil, err
	}
	resolved := &pb.ChannelSource{User: user}
	if source.GetChannelId() == "" {
		return resolved, nil
	}

	channel, changed, err := s.DB.RecordChannel(ctx, b.row.ID, source.GetChannelId())
	if err != nil {
		return nil, fmt.Errorf("record channel: %w", err)
	}
	if err := s.publishChannel(ctx, channel, changed); err != nil {
		return nil, err
	}
	resolved.ChannelId = channel.ID
	return resolved, nil
}

// resolveUser returns a user from a backend with the ID plugins use, recording
// them if they're new.
func (s *Server) resolveUser(ctx context.Context, b *backend, user *pb.User) (*pb.User, error) {
	if user == nil {
		return nil, nil
	}
	if user.GetId() == "" {
		return &pb.User{DisplayName: user.GetDisplayName()}, nil
	}

	recorded, err := s.DB.RecordUser(ctx, b.row.ID, user.GetId(), user.GetDisplayName())
	if err != nil {
		return nil, fmt.Errorf("record user: %w", err)
	}
	return &pb.User{Id: recorded.ID, DisplayName: user.GetDisplayName()}, nil
}
