package core

import (
	"strings"

	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// backendProto describes a backend for plugins.
func backendProto(b Backend) *pb.Backend {
	return &pb.Backend{Id: b.ID, Type: b.Type, Connected: b.Connected}
}

// channelProto describes a channel for plugins. backendConnected says whether
// the channel's backend is connected.
func channelProto(c Channel, backendConnected bool) *pb.Channel {
	state := pb.ChannelState_CHANNEL_STATE_BACKEND_DISCONNECTED
	switch {
	case !backendConnected:
	case c.Joined:
		state = pb.ChannelState_CHANNEL_STATE_JOINED
	default:
		state = pb.ChannelState_CHANNEL_STATE_NOT_JOINED
	}

	// A channel that isn't in a group is its own only member.
	return &pb.Channel{
		Id:          c.ID,
		DisplayName: c.DisplayName,
		Topic:       c.Topic,
		State:       state,
		Members: []*pb.ChannelMember{{
			Id:          c.ID,
			BackendId:   c.BackendID,
			DisplayName: c.DisplayName,
			State:       state,
		}},
	}
}

func backendUpdatedEvent(b Backend) *pb.Event {
	return &pb.Event{Inner: &pb.Event_BackendUpdated{BackendUpdated: &pb.BackendUpdatedEvent{
		Backend: backendProto(b),
	}}}
}

func channelUpdatedEvent(c Channel, backendConnected bool) *pb.Event {
	return &pb.Event{Inner: &pb.Event_ChannelUpdated{ChannelUpdated: &pb.ChannelUpdatedEvent{
		Channel: channelProto(c, backendConnected),
	}}}
}

// idKinds names what each ID prefix refers to, for errors.
var idKinds = map[string]string{
	backendIDPrefix: "backend",
	channelIDPrefix: "channel",
	userIDPrefix:    "user",
}

// checkID checks that a plugin's request names the right kind of thing in
// field, so a user ID sent as a channel gets a clear error.
func checkID(field, id, prefix string) error {
	got, _, ok := strings.Cut(id, "_")
	if ok && got == prefix {
		return nil
	}
	return status.Errorf(codes.InvalidArgument, "invalid %s: %q isn't a %s ID", field, id, idKinds[prefix])
}
