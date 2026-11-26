package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
)

func TestListBackends(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	ids := map[backendKey]string{testBackendKey: env.backendID}
	backends := map[backendKey]*fakeBackend{}
	for _, key := range []backendKey{{"irc", "irc.libera-chat"}, {"discord", "b"}, {"irc", "a"}} {
		backends[key], ids[key] = env.connectBackend(t, key)
	}
	discord := backendKey{"discord", "b"}

	// Ordered by type and then the name each backend connected with.
	want := []*pb.Backend{
		{Id: ids[discord], Type: "discord", Connected: true},
		{Id: ids[backendKey{"irc", "a"}], Type: "irc", Connected: true},
		{Id: ids[backendKey{"irc", "irc.libera-chat"}], Type: "irc", Connected: true},
		{Id: env.backendID, Type: "irc", Connected: true},
	}
	assertProtosEqual(t, want, env.listBackends(t, false))

	// Backends that leave are only listed when asked for.
	backends[discord].cancel()
	env.waitForBackend(t, discord, false)
	assertProtosEqual(t, want[1:], env.listBackends(t, false))

	want[0].Connected = false
	assertProtosEqual(t, want, env.listBackends(t, true))
}

// listBackends returns ListBackends' answer.
func (e *testEnv) listBackends(t *testing.T, includeDisconnected bool) []*pb.Backend {
	t.Helper()

	resp, err := e.plugin.ListBackends(e.ctx, &pb.ListBackendsRequest{IncludeDisconnected: includeDisconnected})
	assert.NoError(t, err)
	return resp.GetBackends()
}

func TestGetBackendInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reply   *pb.ChatEvent
		code    codes.Code
		message string
	}{
		{
			name: "metadata",
			reply: &pb.ChatEvent{Inner: &pb.ChatEvent_Metadata{Metadata: &pb.MetadataChatEvent{
				Values: map[string]string{"command_prefix": "!"},
			}}},
		},
		{
			name:  "failure",
			reply: &pb.ChatEvent{Inner: &pb.ChatEvent_Failed{Failed: &pb.FailedChatEvent{Reason: "not ready"}}},
			code:  codes.Unknown, message: "not ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)
			done := runAsync(func() (*pb.BackendInfoResponse, error) {
				return env.plugin.GetBackendInfo(env.ctx, &pb.BackendInfoRequest{BackendId: env.backendID})
			})

			request, err := env.backend.recv()
			assert.NoError(t, err)
			assert.True(t, request.GetMetadata() != nil, "wrong kind of request")
			env.backend.reply(request, tt.reply)

			got := waitFor(t, done, "result")
			if tt.code != codes.OK {
				assertStatus(t, got.err, tt.code, tt.message)
				return
			}
			assert.NoError(t, got.err)
			assertProtoEqual(t, &pb.Backend{Id: env.backendID, Type: "irc", Connected: true}, got.value.GetBackend())
			assert.Equal(t, map[string]string{"command_prefix": "!"}, got.value.GetMetadata())
		})
	}
}

func TestGetBackendInfoRejectsBadRequests(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	_, err := env.plugin.GetBackendInfo(env.ctx, &pb.BackendInfoRequest{BackendId: "nope"})
	assertStatus(t, err, codes.InvalidArgument, `invalid backend_id: "nope" isn't a backend ID`)

	// A channel's ID isn't a backend's, even though both are real.
	_, err = env.plugin.GetBackendInfo(env.ctx, &pb.BackendInfoRequest{BackendId: env.channelID})
	assertStatus(t, err, codes.InvalidArgument, fmt.Sprintf("invalid backend_id: %q isn't a backend ID", env.channelID))

	_, err = env.plugin.GetBackendInfo(env.ctx, &pb.BackendInfoRequest{BackendId: "sbb_nowhere"})
	assertStatus(t, err, codes.NotFound, "backend not found")
}

func TestGetCoreInfo(t *testing.T) {
	t.Parallel()

	before := time.Now()
	env := startTestEnv(t)

	resp, err := env.plugin.GetCoreInfo(env.ctx, &pb.CoreInfoRequest{})
	after := time.Now()
	assert.NoError(t, err)

	// Whole seconds, so allow for rounding down.
	assert.True(t, resp.GetStartupTimestamp() >= uint64(before.Unix()), "started before the test began")
	assert.True(t, resp.GetStartupTimestamp() <= resp.GetCurrentTimestamp())
	assert.True(t, resp.GetCurrentTimestamp() <= uint64(after.Unix()))
	assert.True(t, resp.GetCurrentTimestamp() >= uint64(before.Unix()))
}
