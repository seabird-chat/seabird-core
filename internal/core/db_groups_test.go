package core

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestCreateChannelGroup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)

	group, err := db.CreateChannelGroup(ctx, "main")
	assert.NoError(t, err)
	assertSeabirdID(t, groupIDPrefix, group.ID)
	assert.Equal(t, "main", group.Name)

	_, err = db.CreateChannelGroup(ctx, "main")
	assert.IsError(t, err, ErrGroupExists)

	_, err = db.CreateChannelGroup(ctx, "")
	assert.EqualError(t, err, "group name must not be empty")

	byID, err := db.GetChannelGroup(ctx, group.ID)
	assert.NoError(t, err)
	assert.Equal(t, group, byID)

	byName, err := db.GetChannelGroupByName(ctx, "main")
	assert.NoError(t, err)
	assert.Equal(t, group, byName)

	_, err = db.GetChannelGroupByName(ctx, "nope")
	assert.IsError(t, err, ErrNotFound)
}

func TestListChannelGroups(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)

	b := createTestGroup(t, db, "b")
	a := createTestGroup(t, db, "a")

	groups, err := db.ListChannelGroups(ctx)
	assert.NoError(t, err)
	assert.Equal(t, []ChannelGroup{*a, *b}, groups)
}

func TestChannelGroupMembers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := connectTestBackend(t, db, "libera")
	oftc := connectTestBackend(t, db, "oftc")
	main := createTestGroup(t, db, "main")
	other := createTestGroup(t, db, "other")

	a := recordTestChannel(t, db, libera, "#a")
	b := recordTestChannel(t, db, libera, "#b")
	c := recordTestChannel(t, db, oftc, "#c")

	assert.NoError(t, db.AddToChannelGroup(ctx, main.ID, b, a))
	assert.Equal(t, []string{a, b}, groupMemberIDs(t, db, main.ID))

	// Adding a member again is fine.
	assert.NoError(t, db.AddToChannelGroup(ctx, main.ID, a))
	assert.Equal(t, []string{a, b}, groupMemberIDs(t, db, main.ID))

	channel, err := db.GetChannel(ctx, a)
	assert.NoError(t, err)
	assert.Equal(t, &main.ID, channel.GroupID)

	// A channel can only be in one group, and a failed add changes nothing.
	err = db.AddToChannelGroup(ctx, other.ID, c, a)
	assert.EqualError(t, err, "channel "+a+" is already in group "+main.ID)
	assert.Equal(t, []string{}, groupMemberIDs(t, db, other.ID))

	err = db.RemoveFromChannelGroup(ctx, main.ID, b, c)
	assert.EqualError(t, err, "channel "+c+" isn't in group "+main.ID)
	assert.Equal(t, []string{a, b}, groupMemberIDs(t, db, main.ID))

	assert.NoError(t, db.RemoveFromChannelGroup(ctx, main.ID, b))
	assert.Equal(t, []string{a}, groupMemberIDs(t, db, main.ID))

	channel, err = db.GetChannel(ctx, b)
	assert.NoError(t, err)
	assert.Zero(t, channel.GroupID)
}

func TestChannelGroupMembersRejectUnknownIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	main := createTestGroup(t, db, "main")

	assert.IsError(t, db.AddToChannelGroup(ctx, main.ID, "sbc_nowhere"), ErrNotFound)
	assert.IsError(t, db.AddToChannelGroup(ctx, "sbg_nowhere"), ErrNotFound)
	assert.IsError(t, db.RemoveFromChannelGroup(ctx, "sbg_nowhere"), ErrNotFound)
}

func TestDeleteChannelGroup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := connectTestBackend(t, db, "libera")
	main := createTestGroup(t, db, "main")
	a := recordTestChannel(t, db, libera, "#a")
	assert.NoError(t, db.AddToChannelGroup(ctx, main.ID, a))

	assert.NoError(t, db.DeleteChannelGroup(ctx, main.ID))
	_, err := db.GetChannelGroup(ctx, main.ID)
	assert.IsError(t, err, ErrNotFound)

	channel, err := db.GetChannel(ctx, a)
	assert.NoError(t, err)
	assert.Zero(t, channel.GroupID, "members are left ungrouped")

	assert.IsError(t, db.DeleteChannelGroup(ctx, main.ID), ErrNotFound)
}

func createTestGroup(t *testing.T, db *DB, name string) *ChannelGroup {
	t.Helper()

	group, err := db.CreateChannelGroup(context.Background(), name)
	assert.NoError(t, err)
	return group
}

// recordTestChannel records a channel on a backend and returns its ID.
func recordTestChannel(t *testing.T, db *DB, backendID, externalID string) string {
	t.Helper()

	channel, _, err := db.RecordChannel(context.Background(), backendID, externalID)
	assert.NoError(t, err)
	return channel.ID
}

func groupMemberIDs(t *testing.T, db *DB, groupID string) []string {
	t.Helper()

	members, err := db.ListGroupMembers(context.Background(), groupID)
	assert.NoError(t, err)
	return channelIDs(members)
}
