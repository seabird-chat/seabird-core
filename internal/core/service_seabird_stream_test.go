package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// registeredCommands returns the names of the registered commands, sorted.
func registeredCommands(t *testing.T, env *testEnv) []string {
	t.Helper()

	resp, err := env.plugin.RegisteredCommands(env.ctx, &pb.CommandsRequest{})
	assert.NoError(t, err)

	names := make([]string, 0, len(resp.GetCommands()))
	for name := range resp.GetCommands() {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestStreamEventsDeliversEverythingToEveryPlugin(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	weather, _ := env.openEvents(t, commandsNamed("weather"))
	other, _ := env.openEvents(t, nil)

	// Chat from a backend, then something a plugin sent.
	env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
		Source: chatSource("#chan", "alice", "Alice"), Command: "weather", Arg: "paris",
	}}})
	env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{Text: "hello"}}})
	result := env.sendMessage(env.ctx, "reply")
	request, err := env.backend.recv()
	assert.NoError(t, err)
	env.backend.reply(request, successEvent(""))
	assert.NoError(t, waitFor(t, result, "result"))

	// Both plugins see all three, including a command only one of them registered,
	// exactly as published.
	for range 3 {
		want := env.events.next(t)
		for name, stream := range map[string]*eventStream{"weather": weather, "other": other} {
			got, err := stream.recv(t)
			assert.NoError(t, err, name)
			assertProtoEqual(t, want, got)
		}
	}
}

func TestStreamEventsKeepOrder(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	stream, _ := env.openEvents(t, nil)

	const count = 20
	for i := range count {
		env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{Text: fmt.Sprint(i)}}})
	}
	for i := range count {
		got, err := stream.recv(t)
		assert.NoError(t, err)
		assert.Equal(t, fmt.Sprint(i), got.GetMessage().GetText())
	}
}

func TestStreamEventsOnlyDeliversWhatHappensAfterward(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{Text: "before"}}})
	env.events.next(t)

	stream, _ := env.openEvents(t, nil)
	env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{Text: "after"}}})

	got, err := stream.recv(t)
	assert.NoError(t, err)
	assert.Equal(t, "after", got.GetMessage().GetText())
}

func TestRegisteredCommands(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	assert.Equal(t, []string{}, registeredCommands(t, env))

	env.openEvents(t, commandsNamed("weather", "time"))
	env.openEvents(t, commandsNamed("help"))

	resp, err := env.plugin.RegisteredCommands(env.ctx, &pb.CommandsRequest{})
	assert.NoError(t, err)
	assert.Equal(t, []string{"help", "time", "weather"}, registeredCommands(t, env))
	// Each command's help is returned as the plugin gave it.
	for name, metadata := range resp.GetCommands() {
		assertProtoEqual(t, commandMetadata(name), metadata)
	}
}

func TestStreamEventsRejectCommandCollisions(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	_, endFirst := env.openEvents(t, commandsNamed("weather", "b", "c"))

	// Several commands collide; the first by name is reported, every time.
	for range 10 {
		stream, err := env.plugin.StreamEvents(env.ctx, &pb.StreamEventsRequest{
			Commands: commandsNamed("alpha", "weather", "c", "b"),
		})
		if err == nil {
			_, err = stream.Recv()
		}
		assertStatus(t, err, codes.AlreadyExists, `command "b" already registered by another plugin`)
	}

	// The rejected plugin registered nothing, not even the commands that were free.
	assert.Equal(t, []string{"b", "c", "weather"}, registeredCommands(t, env))

	// Once the owner is gone, the names are free again.
	endFirst()
	eventually(t, "the commands to be released", func() bool { return len(registeredCommands(t, env)) == 0 })
	env.openEvents(t, commandsNamed("alpha", "weather", "c", "b"))
	assert.Equal(t, []string{"alpha", "b", "c", "weather"}, registeredCommands(t, env))
}

func TestStreamEventsDisconnectSlowPlugins(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	stream, _ := env.openEvents(t, commandsNamed("weather"))

	// Events big enough to fill the connection's window, sent while the plugin
	// isn't reading. The same event is published every time; events are shared.
	huge := &pb.Event{Inner: &pb.Event_Message{Message: &pb.MessageEvent{Text: strings.Repeat("x", 1<<20)}}}
	const published = 200
	for range published {
		env.publishDirectly(t, huge)
	}

	// When the plugin reads again it gets what was already on the wire, and then
	// is told it fell behind.
	received := 0
	var err error
	for {
		if _, err = stream.recv(t); err != nil {
			break
		}
		received++
	}
	assertStatus(t, err, codes.Unavailable, "fell behind reading events")
	assert.True(t, received < published, "the plugin got all %d events", received)
	eventually(t, "the commands to be released", func() bool { return len(registeredCommands(t, env)) == 0 })
}

func TestStreamEventsLeaving(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	stream, leave := env.openEvents(t, commandsNamed("weather", "help"))
	assert.Equal(t, []string{"help", "weather"}, registeredCommands(t, env))

	// Leaving releases the plugin's commands.
	leave()
	_, err := stream.recv(t)
	assert.Equal(t, codes.Canceled, status.Code(err))
	eventually(t, "the commands to be released", func() bool { return len(registeredCommands(t, env)) == 0 })
}
