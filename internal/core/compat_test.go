package core

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	seabird "github.com/seabird-chat/seabird-go"
	"github.com/seabird-chat/seabird-go/pb"
)

// The seabird-go clients are what plugins and backends are built with, so this
// runs a whole exchange through them.
func TestSeabirdGoClients(t *testing.T) {
	t.Parallel()

	conn, _ := serveTestServer(t, newTestServer(t))
	url := "http://" + conn.Target()
	id := BackendID{Type: "irc", ID: "libera"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	plugin, err := seabird.NewClient(url, testToken)
	assert.NoError(t, err)
	t.Cleanup(func() { plugin.Close() })

	// A backend connects.
	ingest, err := seabird.NewChatIngestClient(url, testToken)
	assert.NoError(t, err)
	t.Cleanup(func() { ingest.Close() })
	backend, err := ingest.IngestEvents(id.Type, id.ID)
	assert.NoError(t, err)
	t.Cleanup(func() { backend.Close() })
	eventually(t, "backend to register", func() bool {
		resp, err := plugin.Inner.ListBackends(ctx, &pb.ListBackendsRequest{})
		assert.NoError(t, err)
		return len(resp.GetBackends()) == 1
	})

	// The plugin registers a command and starts reading events. As in
	// openEvents, the backend sends sync events until one arrives.
	events, err := plugin.StreamEvents(map[string]*pb.CommandMetadata{"hello": commandMetadata("hello")})
	assert.NoError(t, err)
	t.Cleanup(func() { events.Close() })
	token := rand.Text()
	for synced, attempts := false, 0; !synced; attempts++ {
		if attempts == 100 {
			t.Fatal("the server never started delivering events")
		}
		assert.NoError(t, backend.Send(syncEvent(token)))
		select {
		case event := <-events.C:
			synced = event.GetTags()[syncTag] == token
		case <-time.After(20 * time.Millisecond):
		}
	}
	next := func(what string) *pb.Event {
		for {
			if event := waitFor(t, events.C, what); event.GetTags()[syncTag] == "" {
				return event
			}
		}
	}

	// Someone runs the command in a channel.
	assert.NoError(t, backend.Send(&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
		Source:  chatSource("#chan", "alice", "Alice"),
		Command: "hello",
		Arg:     "world",
	}}}))
	command := next("the command").GetCommand()
	assert.Equal(t, "hello", command.GetCommand())
	assert.Equal(t, "world", command.GetArg())
	assert.Equal(t, "irc://libera/%23chan", command.GetSource().GetChannelId())
	assert.Equal(t, "irc://libera/alice", command.GetSource().GetUser().GetId())

	// The plugin replies using the source it was given, and the backend is asked
	// to send it to the channel using its own ID for it.
	replied := callAsync(func() error { return plugin.Reply(command.GetSource(), "hi Alice") })
	request := waitFor(t, backend.C, "the request")
	assert.Equal(t, "#chan", request.GetSendMessage().GetChannelId())
	assert.Equal(t, "hi Alice", request.GetSendMessage().GetText())
	assert.Equal(t, "hi Alice", request.GetSendMessage().GetRootBlock().GetPlain())
	assert.NoError(t, backend.Send(&pb.ChatEvent{
		Id:    request.GetId(),
		Inner: &pb.ChatEvent_Success{Success: &pb.SuccessChatEvent{}},
	}))
	assert.NoError(t, waitFor(t, replied, "the reply"))

	// Plugins, this one included, are told what was sent.
	sent := next("the sent message").GetSendMessage()
	assert.Equal(t, testUsername, sent.GetSender())
	assert.Equal(t, "irc://libera/%23chan", sent.GetChannelId())
	assert.Equal(t, "hi Alice", sent.GetText())

	// The introspection calls plugins use. The client adds its own token.
	backends, err := plugin.Inner.ListBackends(ctx, &pb.ListBackendsRequest{})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(backends.GetBackends()))
	assert.Equal(t, "irc://libera", backends.GetBackends()[0].GetId())

	core, err := plugin.Inner.GetCoreInfo(ctx, &pb.CoreInfoRequest{})
	assert.NoError(t, err)
	assert.True(t, core.GetStartupTimestamp() <= core.GetCurrentTimestamp())

	commands, err := plugin.Inner.RegisteredCommands(ctx, &pb.CommandsRequest{})
	assert.NoError(t, err)
	assertProtoEqual(t, commandMetadata("hello"), commands.GetCommands()["hello"])
}
