package core

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
)

// channelRPC describes one of the RPCs that ask a backend to change its channels.
type channelRPC struct {
	name    string
	badID   string // a malformed ID
	badErr  string // the error for badID
	unknown string // an ID for a backend that isn't connected
	call    func(ctx context.Context, c pb.SeabirdClient, id string, tags map[string]string) error
	target  string // an ID that works

	// check asserts that the backend was sent the right request.
	check func(t *testing.T, request *pb.ChatRequest, tags map[string]string)

	// answer is the event, besides Success, that also answers the request.
	answer *pb.ChatEvent
}

var channelRPCs = []channelRPC{
	{
		name:    "JoinChannel",
		badID:   "nope",
		badErr:  `invalid backend_id: missing "://" after the backend type`,
		unknown: "irc://nowhere",
		target:  "irc://libera",
		call: func(ctx context.Context, c pb.SeabirdClient, id string, tags map[string]string) error {
			_, err := c.JoinChannel(ctx, &pb.JoinChannelRequest{BackendId: id, ChannelName: "#new", Tags: tags})
			return err
		},
		check: func(t *testing.T, request *pb.ChatRequest, tags map[string]string) {
			join := request.GetJoinChannel()
			assert.True(t, join != nil, "wrong kind of request")
			assert.Equal(t, "#new", join.GetChannelName())
			assert.Equal(t, tags, join.GetTags())
		},
		answer: &pb.ChatEvent{Inner: &pb.ChatEvent_JoinChannel{JoinChannel: &pb.JoinChannelChatEvent{ChannelId: "#new"}}},
	},
	{
		name:    "LeaveChannel",
		badID:   "irc://libera",
		badErr:  `invalid channel_id: missing "/" before the path`,
		unknown: "irc://nowhere/%23chan",
		target:  "irc://libera/%23chan",
		call: func(ctx context.Context, c pb.SeabirdClient, id string, tags map[string]string) error {
			_, err := c.LeaveChannel(ctx, &pb.LeaveChannelRequest{ChannelId: id, ExitMessage: "bye", Tags: tags})
			return err
		},
		check: func(t *testing.T, request *pb.ChatRequest, tags map[string]string) {
			leave := request.GetLeaveChannel()
			assert.True(t, leave != nil, "wrong kind of request")
			assert.Equal(t, "#chan", leave.GetChannelId())
			assert.Equal(t, tags, leave.GetTags())
		},
		answer: &pb.ChatEvent{Inner: &pb.ChatEvent_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatEvent{ChannelId: "#chan"}}},
	},
	{
		name:    "UpdateChannelInfo",
		badID:   "irc://libera",
		badErr:  `invalid channel_id: missing "/" before the path`,
		unknown: "irc://nowhere/%23chan",
		target:  "irc://libera/%23chan",
		call: func(ctx context.Context, c pb.SeabirdClient, id string, tags map[string]string) error {
			_, err := c.UpdateChannelInfo(ctx, &pb.UpdateChannelInfoRequest{ChannelId: id, Topic: "new topic", Tags: tags})
			return err
		},
		check: func(t *testing.T, request *pb.ChatRequest, tags map[string]string) {
			update := request.GetUpdateChannelInfo()
			assert.True(t, update != nil, "wrong kind of request")
			assert.Equal(t, "#chan", update.GetChannelId())
			assert.Equal(t, "new topic", update.GetTopic())
			assert.Equal(t, tags, update.GetTags())
		},
		answer: &pb.ChatEvent{Inner: &pb.ChatEvent_ChangeChannel{ChangeChannel: &pb.ChangeChannelChatEvent{ChannelId: "#chan"}}},
	},
}

func TestChannelRPCsForwardRequests(t *testing.T) {
	t.Parallel()

	for _, rpc := range channelRPCs {
		for name, reply := range map[string]*pb.ChatEvent{"success": successEvent(""), "matching event": rpc.answer} {
			t.Run(rpc.name+"/"+name, func(t *testing.T) {
				t.Parallel()

				env := startTestEnv(t)
				tags := map[string]string{"plugin/tag": "x"}

				result := callAsync(func() error { return rpc.call(env.ctx, env.plugin, rpc.target, tags) })

				request, err := env.backend.recv()
				assert.NoError(t, err)
				// Tags go through as the plugin gave them.
				rpc.check(t, request, tags)

				env.backend.reply(request, reply)
				assert.NoError(t, waitFor(t, result, "result"))

				// These aren't chat, so plugins aren't told about them.
				env.assertNothingPublished(t)
			})
		}
	}
}

func TestChannelRPCsReportFailures(t *testing.T) {
	t.Parallel()

	for _, rpc := range channelRPCs {
		t.Run(rpc.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)

			result := callAsync(func() error { return rpc.call(env.ctx, env.plugin, rpc.target, nil) })
			request, err := env.backend.recv()
			assert.NoError(t, err)
			env.backend.reply(request, &pb.ChatEvent{Inner: &pb.ChatEvent_Failed{
				Failed: &pb.FailedChatEvent{Reason: "not allowed"},
			}})
			assertStatus(t, waitFor(t, result, "result"), codes.Unknown, "not allowed")

			// A reply meant for some other request is a protocol error.
			result = callAsync(func() error { return rpc.call(env.ctx, env.plugin, rpc.target, nil) })
			request, err = env.backend.recv()
			assert.NoError(t, err)
			env.backend.reply(request, &pb.ChatEvent{Inner: &pb.ChatEvent_Metadata{Metadata: &pb.MetadataChatEvent{}}})
			assertStatus(t, waitFor(t, result, "result"), codes.Internal, "backend sent an unexpected reply")
		})
	}
}

func TestChannelRPCsRejectBadRequests(t *testing.T) {
	t.Parallel()

	for _, rpc := range channelRPCs {
		t.Run(rpc.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)

			err := rpc.call(env.ctx, env.plugin, rpc.badID, nil)
			assertStatus(t, err, codes.InvalidArgument, rpc.badErr)

			err = rpc.call(env.ctx, env.plugin, rpc.unknown, nil)
			assertStatus(t, err, codes.NotFound, "backend not found")
		})
	}
}

// listChannels returns what ListChannels reports for env's backend once the
// events it has sent so far are handled. Events are handled in order, so a
// command sent after them being published means they have been.
func (e *testEnv) listChannels(t *testing.T) []*pb.Channel {
	t.Helper()

	e.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{Command: "sync"}}})
	assert.Equal(t, "sync", e.events.next(t).GetCommand().GetCommand())

	resp, err := e.plugin.ListChannels(e.ctx, &pb.ListChannelsRequest{BackendId: e.id.String()})
	assert.NoError(t, err)
	return resp.GetChannels()
}

func TestChannelState(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	// A backend with no channels yet.
	assert.Equal(t, 0, len(env.listChannels(t)))

	env.backend.send(joinEvent("#b", "B", "topic b"))
	env.backend.send(joinEvent("#a b", "A B", ""))
	env.backend.send(joinEvent("#a", "A", "topic a"))
	env.backend.send(joinEvent("#C", "C", ""))
	env.backend.send(changeEvent("#a", "A2", "topic a2"))
	env.backend.send(changeEvent("#unknown", "X", "x")) // not joined, so ignored
	env.backend.send(leaveEvent("#b"))
	env.backend.send(leaveEvent("#never-joined"))

	// Ordered byte for byte by the backend's ID, with IDs written the way
	// plugins use them.
	assertProtosEqual(t, []*pb.Channel{
		{Id: "irc://libera/%23C", DisplayName: "C"},
		{Id: "irc://libera/%23a", DisplayName: "A2", Topic: "topic a2"},
		{Id: "irc://libera/%23a%20b", DisplayName: "A B"},
	}, env.listChannels(t))

	// Joining a channel again replaces what was known.
	env.backend.send(joinEvent("#a", "A3", ""))
	env.backend.send(leaveEvent("#C"))
	env.backend.send(leaveEvent("#a b"))
	assertProtosEqual(t, []*pb.Channel{{Id: "irc://libera/%23a", DisplayName: "A3"}}, env.listChannels(t))
}

func TestGetChannelInfo(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	env.backend.send(joinEvent("#a b", "A B", "the topic"))
	env.listChannels(t)

	resp, err := env.plugin.GetChannelInfo(env.ctx, &pb.ChannelInfoRequest{ChannelId: "irc://libera/%23a%20b"})
	assert.NoError(t, err)
	assertProtoEqual(t, &pb.Channel{Id: "irc://libera/%23a%20b", DisplayName: "A B", Topic: "the topic"}, resp.GetChannel())

	tests := []struct {
		name    string
		id      string
		code    codes.Code
		message string
	}{
		{"malformed ID", "nope", codes.InvalidArgument, `invalid channel_id: missing "://" after the backend type`},
		{"ID without a path", "irc://libera", codes.InvalidArgument, `invalid channel_id: missing "/" before the path`},
		{"backend not found", "irc://nowhere/%23a", codes.NotFound, "backend not found"},
		{"unknown channel", "irc://libera/%23nope", codes.NotFound, "channel not found"},
		{"ID is exact", "irc://libera/%23A%20B", codes.NotFound, "channel not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := env.plugin.GetChannelInfo(env.ctx, &pb.ChannelInfoRequest{ChannelId: tt.id})
			assertStatus(t, err, tt.code, tt.message)
		})
	}
}

func TestListChannelsRejectsBadRequests(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	_, err := env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{BackendId: "nope"})
	assertStatus(t, err, codes.InvalidArgument, `invalid backend_id: missing "://" after the backend type`)

	_, err = env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{BackendId: "irc://nowhere"})
	assertStatus(t, err, codes.NotFound, "backend not found")
}
