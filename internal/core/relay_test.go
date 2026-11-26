package core

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
)

// relayed waits for the next request to backend, which must be a relay, and
// answers it.
func relayed(t *testing.T, backend *fakeBackend) *pb.ChatRequest {
	t.Helper()

	request, err := backend.recv()
	assert.NoError(t, err)
	backend.reply(request, successEvent(""))
	return request
}

func messageFrom(channel, text string) *pb.ChatEvent {
	return &pb.ChatEvent{Tags: map[string]string{"backend/tag": "x"}, Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
		Source: chatSource(channel, "alice", "Alice"), Text: text,
	}}}
}

func TestRelaysIncludeTheAuthor(t *testing.T) {
	t.Parallel()

	env := startGroupEnv(t)
	env.discord.send(messageFrom("123", "hello"))

	// A backend that doesn't show authors gets them in the message, in bold.
	message := relayed(t, env.backend).GetSendMessage()
	assert.Equal(t, testChannelPath, message.GetChannelId())
	assert.Equal(t, "Alice (discord): hello", message.GetText())
	assertProtoEqual(t, newContainerBlock(
		newBoldBlock(newTextBlock("Alice (discord)")), newTextBlock(": "), newTextBlock("hello"),
	), message.GetRootBlock())
	assert.Equal(t, map[string]string{"backend/tag": "x"}, message.GetTags())
	assert.Zero(t, message.GetAuthor())

	// Plugins see the message once, and never the relay.
	assert.Equal(t, "hello", env.events.next(t).GetMessage().GetText())
	env.assertNothingPublished(t)
}

func TestRelaysToBackendsThatShowAuthors(t *testing.T) {
	t.Parallel()

	env := startGroupEnv(t, pb.BackendFeature_BACKEND_FEATURE_AUTHOR)
	env.backend.send(messageFrom(testChannelPath, "hello"))

	message := relayed(t, env.discord).GetSendMessage()
	assert.Equal(t, "123", message.GetChannelId())
	assert.Equal(t, "hello", message.GetText())
	assertProtoEqual(t, newTextBlock("hello"), message.GetRootBlock())
	assertProtoEqual(t, &pb.User{DisplayName: "Alice"}, message.GetAuthor())
	assertProtoEqual(t, &pb.Backend{Type: "irc"}, message.GetAuthorBackend())

	env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Action{Action: &pb.ActionEvent{
		Source: chatSource(testChannelPath, "alice", "Alice"), Text: "waves",
	}}})
	action := relayed(t, env.discord).GetPerformAction()
	assert.Equal(t, "waves", action.GetText())
	assertProtoEqual(t, &pb.User{DisplayName: "Alice"}, action.GetAuthor())
}

func TestRelaysRebuildWhatWasTyped(t *testing.T) {
	t.Parallel()

	source := chatSource("123", "alice", "Alice")
	tests := []struct {
		name  string
		event *pb.ChatEvent
		want  string
	}{
		{"action", &pb.ChatEvent{Inner: &pb.ChatEvent_Action{Action: &pb.ActionEvent{
			Source: source, Text: "waves",
		}}}, "* Alice (discord) waves"},
		{"command", &pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
			Source: source, Command: "help", Arg: "foo",
		}}}, "Alice (discord): !help foo"},
		{"command without an argument", &pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
			Source: source, Command: "help",
		}}}, "Alice (discord): !help"},
		{"mention", &pb.ChatEvent{Inner: &pb.ChatEvent_Mention{Mention: &pb.MentionEvent{
			Source: source, Text: "hi",
		}}}, "Alice (discord): seabird: hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := startGroupEnv(t)
			env.discord.send(tt.event)
			assert.Equal(t, tt.want, relayed(t, env.backend).GetSendMessage().GetText())
		})
	}
}

func TestRelaysKeepTheirOrder(t *testing.T) {
	t.Parallel()

	env := startGroupEnv(t)
	for _, text := range []string{"one", "two", "three"} {
		env.discord.send(messageFrom("123", text))
	}
	for _, text := range []string{"one", "two", "three"} {
		assert.Equal(t, "Alice (discord): "+text, relayed(t, env.backend).GetSendMessage().GetText())
	}
}

func TestOnlyGroupsRelay(t *testing.T) {
	t.Parallel()

	env := startGroupEnv(t)
	env.discord.send(joinEvent("456", "#solo", ""))
	env.discord.send(messageFrom("456", "alone"))
	env.discord.send(messageFrom("123", "grouped"))

	// The first relay is the grouped message, so the other wasn't relayed.
	assert.Equal(t, "Alice (discord): grouped", relayed(t, env.backend).GetSendMessage().GetText())
}

func TestRelaysSkipDisconnectedMembers(t *testing.T) {
	t.Parallel()

	env := startGroupEnv(t)
	env.backend.cancel()
	env.waitForBackend(t, testBackendKey, false)

	// The message still reaches plugins.
	env.discord.send(messageFrom("123", "hello"))
	for {
		if event := env.events.next(t); event.GetMessage() != nil {
			assert.Equal(t, "hello", event.GetMessage().GetText())
			break
		}
	}
}

func TestFullRelayQueuesDrop(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	b := &backend{relays: make(chan relay, 1)}

	// Neither call waits, and only the first relay is kept.
	server.queueRelay(t.Context(), b, relay{channelID: "first"})
	server.queueRelay(t.Context(), b, relay{channelID: "second"})
	assert.Equal(t, "first", (<-b.relays).channelID)
	assert.Equal(t, 0, len(b.relays))
}
