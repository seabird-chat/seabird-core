package core

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIngestEventsRejectsBadHello(t *testing.T) {
	tests := []struct {
		name  string
		hello *pb.ChatEvent // nil means the backend sends nothing
	}{
		{name: "nothing sent"},
		{name: "other event first", hello: successEvent("")},
		{name: "no backend info", hello: &pb.ChatEvent{Inner: &pb.ChatEvent_Hello{Hello: &pb.HelloChatEvent{}}}},
		{name: "no id", hello: helloEvent(BackendID{Type: "irc"})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn, _ := serveTestServer(t, newTestServer(t))
			f := dialBackend(t, conn)
			if tt.hello != nil {
				f.send(tt.hello)
			}
			assert.NoError(t, f.stream.CloseSend())

			_, err := f.recv()
			assertStatus(t, err, codes.InvalidArgument, "first message must be a hello with a backend type and id")
		})
	}
}

func TestIngestEventsHelloTimeout(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	server.HelloTimeout = 50 * time.Millisecond
	conn, _ := serveTestServer(t, server)

	// A backend that connects and says nothing is dropped.
	f := dialBackend(t, conn)

	_, err := f.recv()
	assertStatus(t, err, codes.DeadlineExceeded, "timed out waiting for hello message")
}

func TestIngestEventsBackendLeaves(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	env.backend.cancel()

	env.waitForBackend(t, env.id, false)
	_, err := env.plugin.GetBackendInfo(env.ctx, &pb.BackendInfoRequest{BackendId: env.id.String()})
	assertStatus(t, err, codes.NotFound, "backend not found")
}

func TestIngestEventsDuplicateID(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	// A second backend can't take the same ID.
	second := dialBackend(t, env.conn)
	second.send(helloEvent(env.id))
	_, err := second.recv()
	assertStatus(t, err, codes.AlreadyExists, "backend irc://libera is already connected")

	// And the first keeps getting the requests.
	result := env.sendMessage(env.ctx, "still yours")
	request, err := env.backend.recv()
	assert.NoError(t, err)
	assert.Equal(t, "still yours", request.GetSendMessage().GetText())
	env.backend.reply(request, successEvent(""))
	assert.NoError(t, waitFor(t, result, "result"))

	// Once the first disconnects, its ID can be used again.
	env.backend.cancel()
	env.waitForBackend(t, env.id, false)
	env.connectBackend(t, env.id)
}

func TestIngestEventsProtocolErrors(t *testing.T) {
	tests := []struct {
		name    string
		event   *pb.ChatEvent
		message string
	}{
		{name: "second hello", event: helloEvent(BackendID{Type: "irc", ID: "x"}), message: "unexpected chat event type"},
		{name: "event without content", event: &pb.ChatEvent{}, message: "missing inner event"},
		{
			name:    "invalid blocks",
			event:   &pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{RootBlock: &pb.Block{}}}},
			message: "unknown block type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)
			env.backend.send(tt.event)

			_, err := env.backend.recv()
			assertStatus(t, err, codes.InvalidArgument, tt.message)
			env.waitForBackend(t, env.id, false)
		})
	}
}

func TestIngestEventsEndWhenTheServerStops(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	assert.NoError(t, env.stop())

	_, err := env.backend.recv()
	assert.Equal(t, codes.Unavailable, status.Code(err))
}
