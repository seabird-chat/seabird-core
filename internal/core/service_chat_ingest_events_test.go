package core

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/protobuf/proto"
)

func TestIngestEventsPublishesChat(t *testing.T) {
	t.Parallel()

	// Expected events are described with the backend's IDs, since externalIDs
	// translates what plugins see back to them.
	aliceSource := chatSource("#chan", "alice", "Alice")
	wantSource := aliceSource
	alice := &pb.User{Id: "alice", DisplayName: "Alice"}
	wantAlice := alice
	backendTags := map[string]string{"backend/tag": "x"}

	text := func(s string) *pb.Block {
		return &pb.Block{Plain: s, Inner: &pb.Block_Text{Text: &pb.TextBlock{Text: s}}}
	}
	// The block a sender provides, and what core makes of it.
	bold := func() *pb.Block { return newBoldBlock(newTextBlock("hi")) }
	wantBold := &pb.Block{Plain: "hi", Inner: &pb.Block_Bold{Bold: &pb.BoldBlock{
		Inner: &pb.Block{Plain: "hi", Inner: &pb.Block_Text{Text: &pb.TextBlock{Text: "hi"}}},
	}}}

	tests := []struct {
		name string
		in   *pb.ChatEvent
		want *pb.Event
	}{
		{
			name: "message with text only",
			in: &pb.ChatEvent{Tags: backendTags, Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
				Source: aliceSource, Text: "hello",
			}}},
			want: &pb.Event{Tags: backendTags, Inner: &pb.Event_Message{Message: &pb.MessageEvent{
				Source: wantSource, Text: "hello", RootBlock: text("hello"),
			}}},
		},
		{
			name: "message with blocks, whose text is ignored",
			in: &pb.ChatEvent{Tags: backendTags, Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
				Source: aliceSource, Text: "ignored", RootBlock: bold(),
			}}},
			want: &pb.Event{Tags: backendTags, Inner: &pb.Event_Message{Message: &pb.MessageEvent{
				Source: wantSource, Text: "hi", RootBlock: wantBold,
			}}},
		},
		{
			name: "private message",
			in: &pb.ChatEvent{Tags: backendTags, Inner: &pb.ChatEvent_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
				Source: alice, Text: "psst",
			}}},
			want: &pb.Event{Tags: backendTags, Inner: &pb.Event_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
				Source: wantAlice, Text: "psst", RootBlock: text("psst"),
			}}},
		},
		{
			name: "mention",
			in: &pb.ChatEvent{Tags: backendTags, Inner: &pb.ChatEvent_Mention{Mention: &pb.MentionEvent{
				Source: aliceSource, RootBlock: bold(),
			}}},
			want: &pb.Event{Tags: backendTags, Inner: &pb.Event_Mention{Mention: &pb.MentionEvent{
				Source: wantSource, Text: "hi", RootBlock: wantBold,
			}}},
		},
		{
			name: "action",
			in: &pb.ChatEvent{Tags: backendTags, Inner: &pb.ChatEvent_Action{Action: &pb.ActionEvent{
				Source: aliceSource, Text: "waves",
			}}},
			want: &pb.Event{Tags: backendTags, Inner: &pb.Event_Action{Action: &pb.ActionEvent{
				Source: wantSource, Text: "waves", RootBlock: text("waves"),
			}}},
		},
		{
			name: "private action",
			in: &pb.ChatEvent{Tags: backendTags, Inner: &pb.ChatEvent_PrivateAction{PrivateAction: &pb.PrivateActionEvent{
				Source: alice, RootBlock: bold(),
			}}},
			want: &pb.Event{Tags: backendTags, Inner: &pb.Event_PrivateAction{PrivateAction: &pb.PrivateActionEvent{
				Source: wantAlice, Text: "hi", RootBlock: wantBold,
			}}},
		},
		{
			// Commands have no text to normalize, so only the backend's tags remain.
			name: "command",
			in: &pb.ChatEvent{Tags: backendTags, Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
				Source: aliceSource, Command: "weather", Arg: "paris france",
			}}},
			want: &pb.Event{Tags: backendTags, Inner: &pb.Event_Command{Command: &pb.CommandEvent{
				Source: wantSource, Command: "weather", Arg: "paris france",
			}}},
		},
		{
			name: "message with no source",
			in:   &pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{Text: "anonymous"}}},
			want: &pb.Event{Inner: &pb.Event_Message{
				Message: &pb.MessageEvent{Text: "anonymous", RootBlock: text("anonymous")},
			}},
		},
		{
			name: "source with no user",
			in: &pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
				Source: &pb.ChannelSource{ChannelId: "#a b"}, Command: "x",
			}}},
			want: &pb.Event{Tags: map[string]string{}, Inner: &pb.Event_Command{Command: &pb.CommandEvent{
				Source: &pb.ChannelSource{ChannelId: "#a b"}, Command: "x",
			}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)

			env.backend.send(tt.in)

			assertProtoEqual(t, tt.want, env.externalIDs(t, env.events.next(t)))
		})
	}
}

func TestIngestEventsDoNotPublishReplies(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	env.backend.send(successEvent("a"))
	env.backend.send(&pb.ChatEvent{Id: "b", Inner: &pb.ChatEvent_Failed{Failed: &pb.FailedChatEvent{Reason: "no"}}})
	env.backend.send(&pb.ChatEvent{Id: "c", Inner: &pb.ChatEvent_Metadata{Metadata: &pb.MetadataChatEvent{}}})
	env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{Command: "after"}}})

	// Replies only answer requests, so the first thing plugins see is the command.
	assert.Equal(t, "after", env.events.next(t).GetCommand().GetCommand())
}

// Only replies answer requests. Chat that happens to carry a request's ID is
// published like any other.
func TestIngestEventsChatDoesNotAnswerRequests(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	result := env.sendMessage(env.ctx, "hi")
	request, err := env.backend.recv()
	assert.NoError(t, err)
	// The plugin's own message is announced first.
	assert.Equal(t, "hi", env.events.next(t).GetSendMessage().GetText())

	env.backend.reply(request, &pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
		Source: chatSource("#chan", "alice", "Alice"),
		Text:   "hello",
	}}})
	assert.Equal(t, "hello", env.events.next(t).GetMessage().GetText())

	// The request is still waiting for its answer.
	env.backend.reply(request, successEvent(""))
	assert.NoError(t, waitFor(t, result, "result"))
}

func TestIngestEventsRecordIDs(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	env.backend.send(joinEvent("#quiet", "Quiet", ""))
	env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
		Source: chatSource("#chan", "alice", "Alice"), Text: "hello",
	}}})
	env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
		Source: &pb.User{Id: "bob", DisplayName: "Bob"}, Text: "psst",
	}}})
	// Events are handled in order, so once the last one is published the
	// others have been recorded.
	assert.Equal(t, "hello", env.events.next(t).GetMessage().GetText())
	assert.Equal(t, "psst", env.events.next(t).GetPrivateMessage().GetText())

	backends, err := env.server.DB.ListBackends(env.ctx, true)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(backends))
	assert.Equal(t, "libera", backends[0].Name)

	channels, err := env.server.DB.ListChannels(env.ctx, "", true)
	assert.NoError(t, err)
	channelNames := map[string]string{}
	for _, channel := range channels {
		assert.Equal(t, backends[0].ID, channel.BackendID)
		channelNames[channel.ExternalID] = channel.DisplayName
	}
	assert.Equal(t, map[string]string{testChannelPath: testChannelPath, "#quiet": "Quiet"}, channelNames)

	users, err := env.server.DB.ListUsers(env.ctx)
	assert.NoError(t, err)
	userNames := map[string]string{}
	for _, user := range users {
		assert.Equal(t, backends[0].ID, user.BackendID)
		userNames[user.ExternalID] = user.DisplayName
	}
	assert.Equal(t, map[string]string{"alice": "Alice", "bob": "Bob"}, userNames)
}

// externalIDs returns a copy of a chat event with the channel and user IDs
// plugins see replaced by what the backend calls them, failing the test if an
// ID isn't one core recorded.
func (e *testEnv) externalIDs(t *testing.T, event *pb.Event) *pb.Event {
	t.Helper()

	event = proto.Clone(event).(*pb.Event)
	user := func(u *pb.User) {
		if u.GetId() == "" {
			return
		}
		recorded, err := e.server.DB.GetUser(context.Background(), u.GetId())
		assert.NoError(t, err, "user %q", u.GetId())
		u.Id = recorded.ExternalID
	}
	source := func(s *pb.ChannelSource) {
		if s.GetChannelId() != "" {
			recorded, err := e.server.DB.GetChannel(context.Background(), s.GetChannelId())
			assert.NoError(t, err, "channel %q", s.GetChannelId())
			s.ChannelId = recorded.ExternalID
		}
		user(s.GetUser())
	}

	switch inner := event.GetInner().(type) {
	case *pb.Event_Message:
		source(inner.Message.GetSource())
	case *pb.Event_Mention:
		source(inner.Mention.GetSource())
	case *pb.Event_Action:
		source(inner.Action.GetSource())
	case *pb.Event_Command:
		source(inner.Command.GetSource())
	case *pb.Event_PrivateMessage:
		user(inner.PrivateMessage.GetSource())
	case *pb.Event_PrivateAction:
		user(inner.PrivateAction.GetSource())
	}
	return event
}
