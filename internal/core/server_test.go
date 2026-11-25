package core

import (
	"context"
	"net"
	"testing"

	"github.com/alecthomas/assert/v2"
)

// listen returns a listener on a free local port, closed when the test ends.
func listen(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	return listener
}

func TestServerServeStopsOnCancel(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	serverErr := runAsync(func() (struct{}, error) { return struct{}{}, server.Serve(ctx, listen(t)) })

	cancel()
	assert.NoError(t, waitFor(t, serverErr, "the server to stop after cancellation").err)

	// Canceling before the serving goroutine has started isn't an error either.
	// Repeat it so that ordering actually happens.
	for range 20 {
		server := newTestServer(t)
		assert.NoError(t, server.Serve(ctx, listen(t)))
	}
}

func TestServerServeErrors(t *testing.T) {
	t.Parallel()

	closed := listen(t)
	assert.NoError(t, closed.Close())
	server := newTestServer(t)
	assert.Error(t, server.Serve(context.Background(), closed))

	db := openTestDB(t)
	for _, server := range []*Server{{Broker: &MemoryBroker{}}, {DB: db}} {
		assert.EqualError(t, server.Serve(context.Background(), listen(t)), "server needs a DB and a Broker")
	}
}

// Only DB and Broker are required; the rest of the server works on defaults.
func TestServerDefaults(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t, func(s *Server) {
		s.Logger = nil
		s.RequestTimeout = 0
		s.HelloTimeout = 0
	})

	result := callAsync(func() error {
		return sendRPCs[0].call(env.ctx, env.plugin, sendRPCs[0].target, "hi", nil, nil)
	})
	request, err := env.backend.recv()
	assert.NoError(t, err)
	env.backend.reply(request, successEvent(""))
	assert.NoError(t, waitFor(t, result, "result"))
}
