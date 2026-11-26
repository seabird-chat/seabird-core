package core

import (
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
)

// Requests are what plugins ask of a backend: core sends them down the
// backend's stream and waits for its reply.

func TestRequestsGetTheirOwnIDs(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	ids := map[string]bool{}
	for range 3 {
		result := env.sendMessage(env.ctx, "hi")
		request, err := env.backend.recv()
		assert.NoError(t, err)
		assert.NotZero(t, request.Id)
		ids[request.Id] = true
		env.backend.reply(request, successEvent(""))
		assert.NoError(t, waitFor(t, result, "result"))
	}
	assert.Equal(t, 3, len(ids))
}

func TestRequestsTimeOut(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t, func(s *Server) { s.RequestTimeout = 50 * time.Millisecond })

	// The backend gets the request but doesn't answer in time.
	result := env.sendMessage(env.ctx, "slow")
	late, err := env.backend.recv()
	assert.NoError(t, err)
	assertStatus(t, waitFor(t, result, "result"), codes.DeadlineExceeded, "request timed out")

	// Answering it now changes nothing, and the next request gets its own answer.
	env.backend.reply(late, successEvent(""))
	result = env.sendMessage(env.ctx, "next")
	request, err := env.backend.recv()
	assert.NoError(t, err)
	assert.Equal(t, "next", request.GetSendMessage().GetText())
	env.backend.reply(request, successEvent(""))
	assert.NoError(t, waitFor(t, result, "result"))
}

func TestRequestsFailWhenTheBackendDisconnects(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t, func(s *Server) { s.RequestTimeout = 30 * time.Second })

	result := env.sendMessage(env.ctx, "hi")
	_, err := env.backend.recv()
	assert.NoError(t, err)
	env.backend.cancel()

	// The caller is told right away rather than after the timeout.
	assertStatus(t, waitFor(t, result, "result"), codes.Unavailable, "backend disconnected")
}

// A backend that stops reading its stream doesn't hold up anything else.
func TestStuckBackend(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t, func(s *Server) { s.RequestTimeout = 30 * time.Second })
	stuckKey := backendKey{Type: "irc", Name: "stuck"}
	stuck, stuckID := env.connectBackend(t, stuckKey)
	stuck.send(joinEvent("#x", "#x", ""))
	stuckChannel := env.waitForChannel(t, stuckID, "#x")

	// Big requests fill the connection's flow-control window and stall sending
	// to it, with the rest queued or waiting for room. Each is about 3MB on the
	// wire (text, block and plain text), and twenty is more than the window and
	// queue hold.
	huge := strings.Repeat("x", 1<<20)
	var waiting []<-chan error
	for range 20 {
		waiting = append(waiting, callAsync(func() error {
			_, err := env.plugin.SendMessage(env.ctx, &pb.SendMessageRequest{ChannelId: stuckChannel, Text: huge})
			return err
		}))
	}
	time.Sleep(100 * time.Millisecond)

	// Other backends still answer requests.
	result := env.sendMessage(env.ctx, "hi")
	request, err := env.backend.recv()
	assert.NoError(t, err)
	env.backend.reply(request, successEvent(""))
	assert.NoError(t, waitFor(t, result, "result"))

	// The stuck backend's own chat still reaches plugins.
	stuck.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{Command: "still here"}}})
	for {
		if event := env.events.next(t); event.GetCommand() != nil {
			assert.Equal(t, "still here", event.GetCommand().GetCommand())
			break
		}
	}

	// When it goes away, everyone waiting on it is told.
	stuck.cancel()
	for _, result := range waiting {
		assertStatus(t, waitFor(t, result, "result"), codes.Unavailable, "backend disconnected")
	}
	env.waitForBackend(t, stuckKey, false)
}
