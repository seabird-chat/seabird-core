package core

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
)

// discordBackendKey is the second backend in a groupEnv.
var discordBackendKey = backendKey{Type: "discord", Name: "guild"}

// groupEnv is a testEnv with a Discord backend in "#main" as well, and a group
// named "main" holding that and testChannelPath.
type groupEnv struct {
	*testEnv

	discord   *fakeBackend
	discordID string

	groupID     string
	discordChan string // the Discord channel's own ID
}

func startGroupEnv(t *testing.T) *groupEnv {
	t.Helper()

	env := &groupEnv{testEnv: startTestEnv(t)}
	env.discord, env.discordID = env.connectBackend(t, discordBackendKey)
	env.discord.send(joinEvent("123", "#main", ""))
	env.discordChan = env.waitForChannel(t, env.discordID, "123")

	group, err := env.server.DB.CreateChannelGroup(context.Background(), "main")
	assert.NoError(t, err)
	env.groupID = group.ID
	assert.NoError(t, env.server.DB.AddToChannelGroup(context.Background(), group.ID, env.channelID, env.discordChan))
	return env
}

// groupProto is how plugins see the group, with each member's state.
func (e *groupEnv) groupProto(state, ircState, discordState pb.ChannelState) *pb.Channel {
	members := []*pb.ChannelMember{
		{Id: e.channelID, BackendId: e.backendID, DisplayName: testChannelPath, State: ircState},
		{Id: e.discordChan, BackendId: e.discordID, DisplayName: "#main", State: discordState},
	}
	// Members are ordered by backend ID, which is random.
	if e.discordID < e.backendID {
		members[0], members[1] = members[1], members[0]
	}
	return &pb.Channel{Id: e.groupID, DisplayName: "main", State: state, Members: members}
}

func TestGroupsReplaceTheirMembers(t *testing.T) {
	t.Parallel()

	env := startGroupEnv(t)
	env.backend.send(joinEvent("#solo", "#solo", ""))
	env.sync(t)
	group := env.groupProto(joined, joined, joined)

	list := func(req *pb.ListChannelsRequest) []*pb.Channel {
		t.Helper()
		resp, err := env.plugin.ListChannels(env.ctx, req)
		assert.NoError(t, err)
		return resp.GetChannels()
	}

	// Whichever backend is listed, the group is there in place of its member.
	solo := env.channel(t, "#solo", "#solo", "", joined)
	got := list(&pb.ListChannelsRequest{BackendId: env.backendID})
	assertProtosEqual(t, []*pb.Channel{group, solo}, got)
	got = list(&pb.ListChannelsRequest{BackendId: env.discordID})
	assertProtosEqual(t, []*pb.Channel{group}, got)

	// It's listed once across every backend.
	got = list(&pb.ListChannelsRequest{})
	assert.Equal(t, 2, len(got))

	// A member's ID refers to its group.
	for _, id := range []string{env.groupID, env.channelID, env.discordChan} {
		resp, err := env.plugin.GetChannelInfo(env.ctx, &pb.ChannelInfoRequest{ChannelId: id})
		assert.NoError(t, err)
		assertProtoEqual(t, group, resp.GetChannel())
	}

	_, err := env.plugin.GetChannelInfo(env.ctx, &pb.ChannelInfoRequest{ChannelId: "sbg_nowhere"})
	assertStatus(t, err, codes.NotFound, "channel not found")
}

func TestGroupState(t *testing.T) {
	t.Parallel()

	env := startGroupEnv(t)
	events, _ := env.openEvents(t, nil)
	events.updates = true
	updated := func(channel *pb.Channel) *pb.Event {
		return &pb.Event{Inner: &pb.Event_ChannelUpdated{ChannelUpdated: &pb.ChannelUpdatedEvent{Channel: channel}}}
	}

	// Joined while any member is.
	env.backend.send(leaveEvent(testChannelPath))
	assertProtoEqual(t, updated(env.groupProto(joined, notJoined, joined)), events.next(t))

	// Not joined once no member is, even with one backend still connected.
	env.discord.cancel()
	assert.True(t, events.next(t).GetBackendUpdated() != nil)
	assertProtoEqual(t, updated(env.groupProto(notJoined, notJoined, disconnected)), events.next(t))

	// Only joined channels are listed by default.
	resp, err := env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{})
	assert.NoError(t, err)
	assert.Equal(t, 0, len(resp.GetChannels()))

	env.backend.send(joinEvent(testChannelPath, testChannelPath, ""))
	assertProtoEqual(t, updated(env.groupProto(joined, joined, disconnected)), events.next(t))

	// Disconnected once every member's backend is.
	env.backend.cancel()
	assert.True(t, events.next(t).GetBackendUpdated() != nil)
	assertProtoEqual(t, updated(env.groupProto(disconnected, disconnected, disconnected)), events.next(t))
}

func TestGroupEventsCarryTheGroupID(t *testing.T) {
	t.Parallel()

	env := startGroupEnv(t)
	env.discord.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
		Source: chatSource("123", "alice", "Alice"), Text: "hello",
	}}})
	got := env.events.next(t).GetMessage()
	assert.Equal(t, "hello", got.GetText())
	assert.Equal(t, env.groupID, got.GetSource().GetChannelId())
}

func TestEmptyGroups(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	group, err := env.server.DB.CreateChannelGroup(context.Background(), "empty")
	assert.NoError(t, err)
	empty := &pb.Channel{Id: group.ID, DisplayName: "empty", State: notJoined}

	resp, err := env.plugin.GetChannelInfo(env.ctx, &pb.ChannelInfoRequest{ChannelId: group.ID})
	assert.NoError(t, err)
	assertProtoEqual(t, empty, resp.GetChannel())

	// Empty groups are on no backend, and only listed with inactive channels.
	list, err := env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{IncludeInactive: true})
	assert.NoError(t, err)
	assertProtosEqual(t, []*pb.Channel{
		env.channel(t, testChannelPath, testChannelPath, "", joined), empty,
	}, list.GetChannels())

	list, err = env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(list.GetChannels()))

	list, err = env.plugin.ListChannels(env.ctx, &pb.ListChannelsRequest{BackendId: env.backendID, IncludeInactive: true})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(list.GetChannels()))
}
