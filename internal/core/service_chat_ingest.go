package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/belak/x/slogx"
	"github.com/seabird-chat/seabird-go/pb"
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

	id, err := receiveHello(ctx, recv, s.helloTimeout())
	if err != nil {
		return err
	}

	b, err := s.backends.register(id)
	if err != nil {
		return err
	}
	defer s.backends.remove(b)

	logger := slogx.FromContext(ctx).With(
		slogx.String("backend_type", id.Type),
		slogx.String("backend_id", id.ID),
	)
	ctx = slogx.WithLogger(ctx, logger)

	logger.InfoContext(ctx, "backend connected")

	// Seabird IDs aren't sent to plugins yet, so failing to record them only
	// costs the mapping, not the connection.
	if recorded, recordErr := s.DB.RecordBackend(ctx, id.Type, id.ID); recordErr != nil {
		logger.WarnContext(ctx, "failed to record backend", slogx.Err(recordErr))
	} else {
		b.rowID = recorded.ID
	}
	defer func() {
		if err != nil {
			logger.InfoContext(ctx, "backend disconnected", slogx.Err(err))
		} else {
			logger.InfoContext(ctx, "backend disconnected")
		}
	}()

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
) (BackendID, error) {
	var event *pb.ChatEvent

	select {
	case got := <-recv:
		if errors.Is(got.err, io.EOF) {
			return BackendID{}, errBadHello
		}
		if got.err != nil {
			return BackendID{}, got.err
		}
		event = got.event
	case <-time.After(timeout):
		return BackendID{}, status.Error(codes.DeadlineExceeded, "timed out waiting for hello message")
	case <-ctx.Done():
		return BackendID{}, ctx.Err()
	}

	// The getters return zero values for anything missing, so one check covers
	// a missing hello, backend info, type or ID.
	info := event.GetHello().GetBackendInfo()
	if info.GetType() == "" || info.GetId() == "" {
		return BackendID{}, errBadHello
	}
	return BackendID{Type: info.GetType(), ID: info.GetId()}, nil
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
		s.recordChannel(ctx, b, join.GetChannelId(), join.GetDisplayName())
		b.joinChannel(channelInfo{ID: join.GetChannelId(), DisplayName: join.GetDisplayName(), Topic: join.GetTopic()})
		deliverReply(ctx, b, event)
		return nil
	case *pb.ChatEvent_LeaveChannel:
		b.leaveChannel(inner.LeaveChannel.GetChannelId())
		deliverReply(ctx, b, event)
		return nil
	case *pb.ChatEvent_ChangeChannel:
		change := inner.ChangeChannel
		s.recordChannel(ctx, b, change.GetChannelId(), change.GetDisplayName())
		b.changeChannel(channelInfo{ID: change.GetChannelId(), DisplayName: change.GetDisplayName(), Topic: change.GetTopic()})
		deliverReply(ctx, b, event)
		return nil

	// Chat becomes events for plugins, with IDs that say which backend they came
	// from and with the text and blocks made consistent.
	case *pb.ChatEvent_Message:
		message := inner.Message
		s.recordSource(ctx, b, message.GetSource())
		block, err := normalizeBlock(message.GetText(), message.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_Message{Message: &pb.MessageEvent{
			Source:    relativeSource(b.id, message.GetSource()),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})
	case *pb.ChatEvent_PrivateMessage:
		message := inner.PrivateMessage
		s.recordUser(ctx, b, message.GetSource())
		block, err := normalizeBlock(message.GetText(), message.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
			Source:    relativeUser(b.id, message.GetSource()),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})
	case *pb.ChatEvent_Mention:
		mention := inner.Mention
		s.recordSource(ctx, b, mention.GetSource())
		block, err := normalizeBlock(mention.GetText(), mention.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_Mention{Mention: &pb.MentionEvent{
			Source:    relativeSource(b.id, mention.GetSource()),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})
	case *pb.ChatEvent_Action:
		action := inner.Action
		s.recordSource(ctx, b, action.GetSource())
		block, err := normalizeBlock(action.GetText(), action.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_Action{Action: &pb.ActionEvent{
			Source:    relativeSource(b.id, action.GetSource()),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})
	case *pb.ChatEvent_PrivateAction:
		action := inner.PrivateAction
		s.recordUser(ctx, b, action.GetSource())
		block, err := normalizeBlock(action.GetText(), action.GetRootBlock())
		if err != nil {
			return err
		}
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_PrivateAction{PrivateAction: &pb.PrivateActionEvent{
			Source:    relativeUser(b.id, action.GetSource()),
			Text:      block.GetPlain(),
			RootBlock: block,
		}}})

	// Commands are already split up, so there is no text to normalize.
	case *pb.ChatEvent_Command:
		command := inner.Command
		s.recordSource(ctx, b, command.GetSource())
		return s.publish(ctx, &pb.Event{Tags: event.GetTags(), Inner: &pb.Event_Command{Command: &pb.CommandEvent{
			Source:  relativeSource(b.id, command.GetSource()),
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

// recordSource records the channel and user a message came from.
func (s *Server) recordSource(ctx context.Context, b *backend, source *pb.ChannelSource) {
	s.recordChannel(ctx, b, source.GetChannelId(), "")
	s.recordUser(ctx, b, source.GetUser())
}

// recordChannel records a channel the backend reported. Failures are logged
// rather than ending the stream.
func (s *Server) recordChannel(ctx context.Context, b *backend, nativeID, displayName string) {
	if b.rowID == "" || nativeID == "" {
		return
	}
	if _, err := s.DB.RecordChannel(ctx, b.rowID, nativeID, displayName); err != nil {
		slogx.FromContext(ctx).WarnContext(ctx, "failed to record channel",
			slogx.String("channel_id", nativeID), slogx.Err(err))
	}
}

// recordUser records a user the backend reported. Failures are logged rather
// than ending the stream.
func (s *Server) recordUser(ctx context.Context, b *backend, user *pb.User) {
	if b.rowID == "" || user.GetId() == "" {
		return
	}
	if _, err := s.DB.RecordUser(ctx, b.rowID, user.GetId(), user.GetDisplayName()); err != nil {
		slogx.FromContext(ctx).WarnContext(ctx, "failed to record user",
			slogx.String("user_id", user.GetId()), slogx.Err(err))
	}
}

// relativeSource rewrites a source from a backend so its IDs include the backend.
func relativeSource(backend BackendID, source *pb.ChannelSource) *pb.ChannelSource {
	if source == nil {
		return nil
	}
	return &pb.ChannelSource{
		ChannelId: backend.Relative(source.GetChannelId()).String(),
		User:      relativeUser(backend, source.GetUser()),
	}
}

// relativeUser rewrites a user from a backend so its ID includes the backend.
func relativeUser(backend BackendID, user *pb.User) *pb.User {
	if user == nil {
		return nil
	}
	return &pb.User{
		Id:          backend.Relative(user.GetId()).String(),
		DisplayName: user.GetDisplayName(),
	}
}
