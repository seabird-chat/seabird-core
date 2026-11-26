package core

import (
	"context"
	"errors"
	"strings"

	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pluginChannel is a channel as plugins see it: a group, or a channel that
// isn't in one, which is its own only member.
type pluginChannel struct {
	id          string
	displayName string
	topic       string
	members     []Channel
}

func ungroupedChannel(c Channel) *pluginChannel {
	return &pluginChannel{id: c.ID, displayName: c.DisplayName, topic: c.Topic, members: []Channel{c}}
}

// joined reports whether any member is joined.
func (c *pluginChannel) joined() bool {
	for _, member := range c.members {
		if member.Joined {
			return true
		}
	}
	return false
}

// resolveChannel looks up the channel a plugin's request names in field. A
// channel in a group resolves to the group.
func (s *Server) resolveChannel(ctx context.Context, field, id string) (*pluginChannel, error) {
	prefix, _, ok := strings.Cut(id, "_")
	switch {
	case !ok:
	case prefix == channelIDPrefix:
		channel, err := s.DB.GetChannel(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil, errChannelNotFound
		}
		if err != nil {
			return nil, err
		}
		return s.pluginChannelOf(ctx, *channel)
	case prefix == groupIDPrefix:
		return s.groupChannel(ctx, id)
	}
	return nil, status.Errorf(codes.InvalidArgument, "invalid %s: %q isn't a channel ID", field, id)
}

// pluginChannelOf returns how plugins see a backend channel.
func (s *Server) pluginChannelOf(ctx context.Context, c Channel) (*pluginChannel, error) {
	if c.GroupID == nil {
		return ungroupedChannel(c), nil
	}
	return s.groupChannel(ctx, *c.GroupID)
}

// groupChannel returns a group as plugins see it.
func (s *Server) groupChannel(ctx context.Context, id string) (*pluginChannel, error) {
	group, err := s.DB.GetChannelGroup(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, errChannelNotFound
	}
	if err != nil {
		return nil, err
	}
	members, err := s.DB.ListGroupMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	return &pluginChannel{id: group.ID, displayName: group.Name, members: members}, nil
}

// channelProto describes a channel for plugins.
func (s *Server) channelProto(c *pluginChannel) *pb.Channel {
	members := make([]*pb.ChannelMember, 0, len(c.members))
	for _, member := range c.members {
		members = append(members, &pb.ChannelMember{
			Id:          member.ID,
			BackendId:   member.BackendID,
			DisplayName: member.DisplayName,
			State:       s.memberState(member),
		})
	}

	return &pb.Channel{
		Id:          c.id,
		DisplayName: c.displayName,
		Topic:       c.topic,
		State:       channelState(members),
		Members:     members,
	}
}

// memberState says whether messages can currently be sent to a backend
// channel.
func (s *Server) memberState(c Channel) pb.ChannelState {
	switch {
	case s.backends.get(c.BackendID) == nil:
		return pb.ChannelState_CHANNEL_STATE_BACKEND_DISCONNECTED
	case c.Joined:
		return pb.ChannelState_CHANNEL_STATE_JOINED
	default:
		return pb.ChannelState_CHANNEL_STATE_NOT_JOINED
	}
}

// channelState combines members' states: joined if any is, disconnected if
// every one is, and not joined otherwise, including when there are none.
func channelState(members []*pb.ChannelMember) pb.ChannelState {
	disconnected := len(members) > 0
	for _, member := range members {
		switch member.GetState() {
		case pb.ChannelState_CHANNEL_STATE_JOINED:
			return pb.ChannelState_CHANNEL_STATE_JOINED
		case pb.ChannelState_CHANNEL_STATE_BACKEND_DISCONNECTED:
		default:
			disconnected = false
		}
	}
	if disconnected {
		return pb.ChannelState_CHANNEL_STATE_BACKEND_DISCONNECTED
	}
	return pb.ChannelState_CHANNEL_STATE_NOT_JOINED
}

// listChannels returns the channels plugins see with a member on backendID, or
// on any backend if it's empty. Only channels with a joined member are listed
// unless includeInactive is set. They are in the order of their first member,
// with empty groups last.
func (s *Server) listChannels(ctx context.Context, backendID string, includeInactive bool) ([]*pluginChannel, error) {
	rows, err := s.DB.ListChannels(ctx, backendID, true)
	if err != nil {
		return nil, err
	}

	var channels []*pluginChannel
	seenGroups := make(map[string]bool)
	for _, row := range rows {
		if row.GroupID != nil {
			if seenGroups[*row.GroupID] {
				continue
			}
			seenGroups[*row.GroupID] = true
		}

		channel, err := s.pluginChannelOf(ctx, row)
		if err != nil {
			return nil, err
		}
		if includeInactive || channel.joined() {
			channels = append(channels, channel)
		}
	}

	// Every grouped channel was listed above, so any group not seen yet is
	// empty. Empty groups aren't on any backend, and are never joined.
	if backendID != "" || !includeInactive {
		return channels, nil
	}
	groups, err := s.DB.ListChannelGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		if !seenGroups[group.ID] {
			channels = append(channels, &pluginChannel{id: group.ID, displayName: group.Name})
		}
	}
	return channels, nil
}

// channelUpdatedEvent tells plugins about the channel a backend channel is
// part of.
func (s *Server) channelUpdatedEvent(c *pluginChannel) *pb.Event {
	return &pb.Event{Inner: &pb.Event_ChannelUpdated{ChannelUpdated: &pb.ChannelUpdatedEvent{
		Channel: s.channelProto(c),
	}}}
}
