package core

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	seabird "github.com/seabird-chat/seabird-go"
	"github.com/seabird-chat/seabird-go/pb"
)

func TestIngestEventsPublishesChat(t *testing.T) {
	t.Parallel()

	aliceSource := chatSource("#chan", "alice", "Alice")
	wantSource := chatSource("irc://libera/%23chan", "irc://libera/alice", "Alice")
	alice := &pb.User{Id: "alice", DisplayName: "Alice"}
	wantAlice := &pb.User{Id: "irc://libera/alice", DisplayName: "Alice"}
	backendTags := map[string]string{"backend/tag": "x"}

	text := func(s string) *pb.Block {
		return &pb.Block{Plain: s, Inner: &pb.Block_Text{Text: &pb.TextBlock{Text: s}}}
	}
	// The block a sender provides, and what core makes of it.
	bold := func() *pb.Block { return seabird.NewBoldBlock(seabird.NewTextBlock("hi")) }
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
				Source: &pb.ChannelSource{ChannelId: "irc://libera/%23a%20b"}, Command: "x",
			}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)

			env.backend.send(tt.in)

			assertProtoEqual(t, tt.want, env.events.next(t))
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
