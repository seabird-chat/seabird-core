package seabird

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/seabird-irc/seabird-core/pb"
)

// Ensure that Server implements the ChatIngestServer interface
var _ pb.ChatIngestServer = (*Server)(nil)

func (cs *ChatStream) handleRequests(ctx context.Context, stream pb.ChatIngest_IngestEventsServer) error {
	for {
		rawMsg, ok := cs.requestBoxHandle.Recv(ctx)
		if !ok {
			return status.Error(codes.Canceled, "stream closed")
		}

		req, ok := rawMsg.(*pb.ChatRequest)
		if !ok {
			continue
		}

		err := stream.Send(req)
		if err != nil {
			return err
		}
	}
}

func (cs *ChatStream) handleEvents(ctx context.Context, stream pb.ChatIngest_IngestEventsServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}

		fmt.Printf("%+v\n", msg.Inner)

		var event interface{}

		switch v := msg.Inner.(type) {
		// Success and failed should be ignored. They should only be sent in
		// response to requests and as such, should not be broadcasted.
		case *pb.ChatEvent_Success:
			cs.requestResponseBox.Send(msg.Id, v.Success)
		case *pb.ChatEvent_Failed:
			cs.requestResponseBox.Send(msg.Id, v.Failed)

		case *pb.ChatEvent_Action:
			inner := v.Action

			inner.Source.ChannelId = cs.RelativeID(inner.Source.ChannelId)
			inner.Source.User.Id = cs.RelativeID(inner.Source.User.Id)

			event = inner
		case *pb.ChatEvent_PrivateAction:
			inner := v.PrivateAction

			inner.Source.Id = cs.RelativeID(inner.Source.Id)

			event = inner
		case *pb.ChatEvent_Message:
			inner := v.Message

			inner.Source.ChannelId = cs.RelativeID(inner.Source.ChannelId)
			inner.Source.User.Id = cs.RelativeID(inner.Source.User.Id)

			event = inner
		case *pb.ChatEvent_PrivateMessage:
			inner := v.PrivateMessage

			inner.Source.Id = cs.RelativeID(inner.Source.Id)

			event = inner
		case *pb.ChatEvent_Mention:
			inner := v.Mention

			inner.Source.ChannelId = cs.RelativeID(inner.Source.ChannelId)
			inner.Source.User.Id = cs.RelativeID(inner.Source.User.Id)

			event = inner
		case *pb.ChatEvent_Command:
			inner := v.Command

			inner.Source.ChannelId = cs.RelativeID(inner.Source.ChannelId)
			inner.Source.User.Id = cs.RelativeID(inner.Source.User.Id)

			// TODO: handle help command

			event = inner

		case *pb.ChatEvent_JoinChannel:
			cs.channelLock.Lock()
			cs.channels[v.JoinChannel.ChannelId] = &ChatChannel{
				ID:          v.JoinChannel.ChannelId,
				DisplayName: v.JoinChannel.DisplayName,
				Topic:       v.JoinChannel.Topic,
			}
			cs.channelLock.Unlock()
		case *pb.ChatEvent_LeaveChannel:
			cs.channelLock.Lock()
			delete(cs.channels, v.LeaveChannel.ChannelId)
			cs.channelLock.Unlock()
		case *pb.ChatEvent_ChangeChannel:
			cs.channelLock.Lock()
			if channel, ok := cs.channels[v.ChangeChannel.ChannelId]; ok {
				channel.DisplayName = v.ChangeChannel.DisplayName
				channel.Topic = v.ChangeChannel.Topic
			}
			cs.channelLock.Unlock()
		default:
			// This handles *pb.ChatEvent_Hello and unknowns.
			return status.Errorf(codes.InvalidArgument, "unexpected chat event type: %T", v)
		}

		if event != nil {
			cs.eventBox.Broadcast(event)

			if msg.Id != "" {
				cs.requestResponseBox.Send(msg.Id, event)
			}
		}
	}
}

func (s *Server) IngestEvents(stream pb.ChatIngest_IngestEventsServer) error {
	// Flush the headers. This is not strictly required, but it makes it easier
	// to write clients for some languages because you can start the stream (and
	// start writing to it) without waiting for the server to send messages.
	err := stream.SendHeader(nil)
	if err != nil {
		return err
	}

	// Read in a hello event
	msg, err := stream.Recv()
	if err != nil {
		return err
	}

	helloMsg, ok := msg.GetInner().(*pb.ChatEvent_Hello)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing hello message")
	}

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	cs, err := s.NewChatStream(helloMsg.Hello.BackendInfo.Type, helloMsg.Hello.BackendInfo.Id, cancel)
	if err != nil {
		return err
	}
	defer cs.Close()

	errGroup, ctx := errgroup.WithContext(ctx)

	// Run both sides of the connection and pass both the cancelFunc. If an
	// error is returned, it will be handled by the errGroup. If either one of
	// them returns without an error, they'll cause the context to be cancelled,
	// this method to complete, and the connection to be closed.
	errGroup.Go(func() error { return cs.handleRequests(ctx, stream) })
	errGroup.Go(func() error { return cs.handleEvents(ctx, stream) })

	return errGroup.Wait()
}
