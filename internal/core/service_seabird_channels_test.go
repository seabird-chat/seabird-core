package core

import (
	"context"
	"fmt"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
)

// channelRPC describes one of the RPCs that ask a backend to change its channels.
type channelRPC struct {
	name string

	// field is the request field naming what the RPC acts on, kind is what
	// it names, and target returns an ID for it that works.
	field  string
	kind   string
	target func(e *testEnv) string

	call func(ctx context.Context, c pb.SeabirdClient, id string, tags map[string]string) error

	// check asserts that the backend was sent the right request.
	check func(t *testing.T, request *pb.ChatRequest, tags map[string]string)

	// answer is the event, besides Success, that also answers the request.
	answer *pb.ChatEvent
}

var channelRPCs = []channelRPC{
	{
		name:   "JoinChannel",
		field:  "backend_id",
		kind:   "backend",
		target: func(e *testEnv) string { return e.backendID },
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
		name:   "LeaveChannel",
		field:  "channel_id",
		kind:   "channel",
		target: func(e *testEnv) string { return e.channelID },
		call: func(ctx context.Context, c pb.SeabirdClient, id string, tags map[string]string) error {
			_, err := c.LeaveChannel(ctx, &pb.LeaveChannelRequest{ChannelId: id, ExitMessage: "bye", Tags: tags})
			return err
		},
		check: func(t *testing.T, request *pb.ChatRequest, tags map[string]string) {
			leave := request.GetLeaveChannel()
			assert.True(t, leave != nil, "wrong kind of request")
			assert.Equal(t, testChannelPath, leave.GetChannelId())
			assert.Equal(t, tags, leave.GetTags())
		},
		answer: &pb.ChatEvent{Inner: &pb.ChatEvent_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatEvent{ChannelId: testChannelPath}}},
	},
	{
		name:   "UpdateChannelInfo",
		field:  "channel_id",
		kind:   "channel",
		target: func(e *testEnv) string { return e.channelID },
		call: func(ctx context.Context, c pb.SeabirdClient, id string, tags map[string]string) error {
			_, err := c.UpdateChannelInfo(ctx, &pb.UpdateChannelInfoRequest{ChannelId: id, Topic: "new topic", Tags: tags})
			return err
		},
		check: func(t *testing.T, request *pb.ChatRequest, tags map[string]string) {
			update := request.GetUpdateChannelInfo()
			assert.True(t, update != nil, "wrong kind of request")
			assert.Equal(t, testChannelPath, update.GetChannelId())
			assert.Equal(t, "new topic", update.GetTopic())
			assert.Equal(t, tags, update.GetTags())
		},
		answer: &pb.ChatEvent{Inner: &pb.ChatEvent_ChangeChannel{ChangeChannel: &pb.ChangeChannelChatEvent{ChannelId: testChannelPath}}},
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

				result := callAsync(func() error { return rpc.call(env.ctx, env.plugin, rpc.target(env), tags) })

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

			result := callAsync(func() error { return rpc.call(env.ctx, env.plugin, rpc.target(env), nil) })
			request, err := env.backend.recv()
			assert.NoError(t, err)
			env.backend.reply(request, &pb.ChatEvent{Inner: &pb.ChatEvent_Failed{
				Failed: &pb.FailedChatEvent{Reason: "not allowed"},
			}})
			assertStatus(t, waitFor(t, result, "result"), codes.Unknown, "not allowed")

			// A reply meant for some other request is a protocol error.
			result = callAsync(func() error { return rpc.call(env.ctx, env.plugin, rpc.target(env), nil) })
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

			err := rpc.call(env.ctx, env.plugin, "nope", nil)
			assertStatus(t, err, codes.InvalidArgument, fmt.Sprintf(`invalid %s: "nope" isn't a %s ID`, rpc.field, rpc.kind))

			// Real IDs of the wrong kind are rejected the same way.
			wrong := env.channelID
			if rpc.kind == "channel" {
				wrong = env.backendID
			}
			err = rpc.call(env.ctx, env.plugin, wrong, nil)
			assertStatus(t, err, codes.InvalidArgument, fmt.Sprintf("invalid %s: %q isn't a %s ID", rpc.field, wrong, rpc.kind))

			err = rpc.call(env.ctx, env.plugin, idPrefixes[rpc.kind]+"_nowhere", nil)
			assertStatus(t, err, codes.NotFound, rpc.kind+" not found")

			// Known, but with its backend gone.
			env.backend.cancel()
			env.waitForBackend(t, testBackendKey, false)
			err = rpc.call(env.ctx, env.plugin, rpc.target(env), nil)
			assertStatus(t, err, codes.Unavailable, "backend not connected")
		})
	}
}

// idPrefixes is the ID prefix for each kind of thing.
var idPrefixes = map[string]string{"backend": backendIDPrefix, "channel": channelIDPrefix, "user": userIDPrefix}

// sync waits until everything env's backend has sent so far is handled. Events
// are handled in order, so a command sent after them being published means they
// have been.
func (e *testEnv) sync(t *testing.T) {
	t.Helper()

	e.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{Command: "sync"}}})
	for {
		if e.events.next(t).GetCommand().GetCommand() == "sync" {
			return
		}
	}
}

// listChannels returns what ListChannels reports for env's backend once the
// events it has sent so far are handled.
func (e *testEnv) listChannels(t *testing.T, includeInactive bool) []*pb.Channel {
	t.Helper()

	e.sync(t)
	resp, err := e.plugin.ListChannels(e.ctx, &pb.ListChannelsRequest{
		BackendId: e.backendID, IncludeInactive: includeInactive,
	})
	assert.NoError(t, err)
	return resp.GetChannels()
}

// channel describes one of env's backend's channels as plugins see it, by what
// the backend calls it.
func (e *testEnv) channel(t *testing.T, externalID, name, topic string, state pb.ChannelState) *pb.Channel {
	t.Helper()

	return ungroupedProto(e.channelIDFor(t, e.backendID, externalID), e.backendID, name, topic, state)
}

// ungroupedProto is how plugins see a channel that isn't in a group, which
// is its own only member.
func ungroupedProto(id, backendID, name, topic string, state pb.ChannelState) *pb.Channel {
	return &pb.Channel{
		Id:          id,
		DisplayName: name,
		Topic:       topic,
		State:       state,
		Members: []*pb.ChannelMember{{
			Id: id, BackendId: backendID, DisplayName: name, State: state,
		}},
	}
}

const (
	joined       = pb.ChannelState_CHANNEL_STATE_JOINED
	notJoined    = pb.ChannelState_CHANNEL_STATE_NOT_JOINED
	disconnected = pb.ChannelState_CHANNEL_STATE_BACKEND_DISCONNECTED
)

func TestChannelState(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	var got []*pb.Channel
	main := func(state pb.ChannelState) *pb.Channel {
		return env.channel(t, testChannelPath, testChannelPath, "", state)
	}

	got = env.listChannels(t, false)
	assertProtosEqual(t, []*pb.Channel{main(joined)}, got)

	env.backend.send(joinEvent("#b", "B", "topic b"))
	env.backend.send(joinEvent("#a b", "A B", ""))
	env.backend.send(joinEvent("#a", "A", "topic a"))
	env.backend.send(joinEvent("#C", "C", ""))
	env.backend.send(changeEvent("#a", "A2", "topic a2"))
	env.backend.send(changeEvent("#unknown", "X", "x")) // recorded, but not joined
	env.backend.send(leaveEvent("#b"))
	env.backend.send(leaveEvent("#never-seen")) // not recorded at all

	// Ordered byte for byte by what the backend calls them.
	got = env.listChannels(t, false)
	assertProtosEqual(t, []*pb.Channel{
		env.channel(t, "#C", "C", "", joined),
		env.channel(t, "#a", "A2", "topic a2", joined),
		env.channel(t, "#a b", "A B", "", joined),
		main(joined),
	}, got)

	// Channels it isn't in are only listed when asked for.
	got = env.listChannels(t, true)
	assertProtosEqual(t, []*pb.Channel{
		env.channel(t, "#C", "C", "", joined),
		env.channel(t, "#a", "A2", "topic a2", joined),
		env.channel(t, "#a b", "A B", "", joined),
		env.channel(t, "#b", "B", "topic b", notJoined),
		main(joined),
		env.channel(t, "#unknown", "X", "x", notJoined),
	}, got)

	// Joining a channel again replaces its topic.
	env.backend.send(joinEvent("#a", "A3", ""))
	env.backend.send(leaveEvent("#C"))
	env.backend.send(leaveEvent("#a b"))
	got = env.listChannels(t, false)
	assertProtosEqual(t, []*pb.Channel{
		env.channel(t, "#a", "A3", "", joined),
		main(joined),
	}, got)
}

func TestListChannelsAcrossBackends(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	other, otherID := env.connectBackend(t, backendKey{Type: "irc", Name: "oftc"})
	other.send(joinEvent("#other", "#other", ""))
	otherChannel := env.waitForChannel(t, otherID, "#other")

	// An empty backend ID lists every backend's channels.
	resp, err := env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{})
	assert.NoError(t, err)
	var ids []string
	for _, channel := range resp.GetChannels() {
		ids = append(ids, channel.GetId())
	}
	assert.Equal(t, 2, len(ids))
	assert.SliceContains(t, ids, env.channelID)
	assert.SliceContains(t, ids, otherChannel)

	// A backend that leaves takes its channels with it, unless asked for.
	other.cancel()
	env.waitForBackend(t, backendKey{Type: "irc", Name: "oftc"}, false)
	resp, err = env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{BackendId: otherID})
	assert.NoError(t, err)
	assert.Equal(t, 0, len(resp.GetChannels()))

	resp, err = env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{BackendId: otherID, IncludeInactive: true})
	assert.NoError(t, err)
	assertProtosEqual(t, []*pb.Channel{
		ungroupedProto(otherChannel, otherID, "#other", "", disconnected),
	}, resp.GetChannels())
}

func TestChannelUpdates(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	events, _ := env.openEvents(t, nil)
	events.updates = true
	next := func() *pb.Event {
		t.Helper()
		return events.next(t)
	}
	var got *pb.Event
	updated := func(channel *pb.Channel) *pb.Event {
		return &pb.Event{Inner: &pb.Event_ChannelUpdated{ChannelUpdated: &pb.ChannelUpdatedEvent{Channel: channel}}}
	}

	env.backend.send(joinEvent("#new", "New", "hi"))
	got = next()
	assertProtoEqual(t, updated(env.channel(t, "#new", "New", "hi", joined)), got)

	// Nothing about the channel changed, so plugins hear nothing.
	env.backend.send(joinEvent("#new", "New", "hi"))
	env.backend.send(changeEvent("#new", "New", "hi"))

	env.backend.send(changeEvent("#new", "Renamed", "hi"))
	got = next()
	assertProtoEqual(t, updated(env.channel(t, "#new", "Renamed", "hi", joined)), got)

	env.backend.send(leaveEvent("#new"))
	got = next()
	assertProtoEqual(t, updated(env.channel(t, "#new", "Renamed", "hi", notJoined)), got)

	// A channel core first hears about from a message is announced before it.
	env.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
		Source: chatSource("#seen", "alice", "Alice"), Text: "hello",
	}}})
	got = next()
	assertProtoEqual(t, updated(env.channel(t, "#seen", "", "", notJoined)), got)
	assert.Equal(t, "hello", next().GetMessage().GetText())

	// When the backend leaves, plugins hear about it, then about each channel
	// it was in.
	env.backend.cancel()
	assertProtoEqual(t, &pb.Event{Inner: &pb.Event_BackendUpdated{BackendUpdated: &pb.BackendUpdatedEvent{
		Backend: &pb.Backend{Id: env.backendID, Type: "irc"},
	}}}, next())
	got = next()
	assertProtoEqual(t, updated(env.channel(t, testChannelPath, testChannelPath, "", disconnected)), got)

	// And when it comes back.
	env.backend, _ = env.connectBackend(t, testBackendKey)
	assertProtoEqual(t, &pb.Event{Inner: &pb.Event_BackendUpdated{BackendUpdated: &pb.BackendUpdatedEvent{
		Backend: &pb.Backend{Id: env.backendID, Type: "irc", Connected: true},
	}}}, next())
	env.backend.send(joinEvent(testChannelPath, testChannelPath, ""))
	got = next()
	assertProtoEqual(t, updated(env.channel(t, testChannelPath, testChannelPath, "", joined)), got)
}

func TestGetChannelInfo(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	env.backend.send(joinEvent("#a b", "A B", "the topic"))
	env.backend.send(changeEvent("#elsewhere", "Elsewhere", ""))
	env.sync(t)

	info := func(id string) *pb.Channel {
		t.Helper()
		resp, err := env.plugin.GetChannelInfo(env.ctx, &pb.ChannelInfoRequest{ChannelId: id})
		assert.NoError(t, err)
		return resp.GetChannel()
	}

	// Any channel core knows about, whatever its state.
	joinedChannel := env.channel(t, "#a b", "A B", "the topic", joined)
	assertProtoEqual(t, joinedChannel, info(joinedChannel.GetId()))
	elsewhere := env.channel(t, "#elsewhere", "Elsewhere", "", notJoined)
	assertProtoEqual(t, elsewhere, info(elsewhere.GetId()))

	tests := []struct {
		name    string
		id      string
		code    codes.Code
		message string
	}{
		{"malformed ID", "nope", codes.InvalidArgument, `invalid channel_id: "nope" isn't a channel ID`},
		{"backend ID", env.backendID, codes.InvalidArgument, fmt.Sprintf("invalid channel_id: %q isn't a channel ID", env.backendID)},
		{"unknown channel", "sbc_nope", codes.NotFound, "channel not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := env.plugin.GetChannelInfo(env.ctx, &pb.ChannelInfoRequest{ChannelId: tt.id})
			assertStatus(t, err, tt.code, tt.message)
		})
	}

	env.backend.cancel()
	env.waitForBackend(t, testBackendKey, false)
	joinedChannel.State = disconnected
	joinedChannel.Members[0].State = disconnected
	assertProtoEqual(t, joinedChannel, info(joinedChannel.GetId()))
}

func TestListChannelsRejectsBadRequests(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	_, err := env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{BackendId: "nope"})
	assertStatus(t, err, codes.InvalidArgument, `invalid backend_id: "nope" isn't a backend ID`)

	_, err = env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{BackendId: "sbb_nowhere"})
	assertStatus(t, err, codes.NotFound, "backend not found")
}
