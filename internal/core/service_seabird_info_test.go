package core

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
)

func TestListBackends(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	others := []BackendID{{"irc", "irc.libera-chat"}, {"discord", "b"}, {"irc", "a"}}
	var discord *fakeBackend
	for _, id := range others {
		f := env.connectBackend(t, id)
		if id.Type == "discord" {
			discord = f
		}
	}

	// Ordered by type and then ID, with IDs written the way plugins use them.
	assert.Equal(t, []string{
		"discord discord://b",
		"irc irc://a",
		"irc irc://irc%2Elibera%2Dchat",
		"irc irc://libera",
	}, env.listBackends(t))

	// Backends that leave are no longer listed.
	discord.cancel()
	env.waitForBackend(t, others[1], false)
	assert.Equal(t, 3, len(env.listBackends(t)))
}

// listBackends returns ListBackends' answer as "type id" pairs.
func (e *testEnv) listBackends(t *testing.T) []string {
	t.Helper()

	resp, err := e.plugin.ListBackends(e.ctx, &pb.ListBackendsRequest{})
	assert.NoError(t, err)

	var got []string
	for _, backend := range resp.GetBackends() {
		got = append(got, backend.GetType()+" "+backend.GetId())
	}
	return got
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
				return env.plugin.GetBackendInfo(env.ctx, &pb.BackendInfoRequest{BackendId: "irc://libera"})
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
			assert.Equal(t, "irc://libera", got.value.GetBackend().GetId())
			assert.Equal(t, "irc", got.value.GetBackend().GetType())
			assert.Equal(t, map[string]string{"command_prefix": "!"}, got.value.GetMetadata())
		})
	}
}

func TestGetBackendInfoRejectsBadRequests(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	_, err := env.plugin.GetBackendInfo(env.ctx, &pb.BackendInfoRequest{BackendId: "nope"})
	assertStatus(t, err, codes.InvalidArgument, `invalid backend_id: missing "://" after the backend type`)

	_, err = env.plugin.GetBackendInfo(env.ctx, &pb.BackendInfoRequest{BackendId: "irc://nowhere"})
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

func TestGetBackendInfoAnswersWithTheCanonicalID(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	id := BackendID{Type: "irc", ID: "irc.libera-chat"}
	f := env.connectBackend(t, id)

	// Plugins can write the ID without escaping; the answer uses the escaped form.
	done := runAsync(func() (*pb.BackendInfoResponse, error) {
		return env.plugin.GetBackendInfo(env.ctx, &pb.BackendInfoRequest{BackendId: "irc://irc.libera-chat"})
	})
	request, err := f.recv()
	assert.NoError(t, err)
	f.reply(request, &pb.ChatEvent{Inner: &pb.ChatEvent_Metadata{Metadata: &pb.MetadataChatEvent{}}})

	got := waitFor(t, done, "result")
	assert.NoError(t, got.err)
	assert.Equal(t, "irc://irc%2Elibera%2Dchat", got.value.GetBackend().GetId())
	assert.Equal(t, "irc", got.value.GetBackend().GetType())
}
