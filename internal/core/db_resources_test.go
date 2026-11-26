package core

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestConnectBackend(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)

	backend, err := db.ConnectBackend(ctx, "irc", "libera", "")
	assert.NoError(t, err)
	assertSeabirdID(t, backendIDPrefix, backend.ID)
	assert.Equal(t, "irc", backend.Type)
	assert.Equal(t, "libera", backend.Name)
	assert.True(t, backend.Connected)
	assert.NotZero(t, backend.LastSeen)

	again, err := db.ConnectBackend(ctx, "irc", "libera", "")
	assert.NoError(t, err)
	assert.Equal(t, backend.ID, again.ID, "a backend keeps its ID")

	other, err := db.ConnectBackend(ctx, "discord", "libera", "")
	assert.NoError(t, err)
	assert.NotEqual(t, backend.ID, other.ID, "backends of different types are different backends")

	// The display name is whatever the latest hello gave.
	named, err := db.ConnectBackend(ctx, "irc", "libera", "Libera")
	assert.NoError(t, err)
	assert.Equal(t, "Libera", named.DisplayName)
	unnamed, err := db.ConnectBackend(ctx, "irc", "libera", "")
	assert.NoError(t, err)
	assert.Equal(t, "", unnamed.DisplayName)
}

func TestDisconnectBackend(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := connectTestBackend(t, db, "libera")
	oftc := connectTestBackend(t, db, "oftc")

	b, _, err := db.JoinChannel(ctx, libera, "#b", "", "")
	assert.NoError(t, err)
	a, _, err := db.JoinChannel(ctx, libera, "#a", "", "")
	assert.NoError(t, err)
	_, _, err = db.RecordChannel(ctx, libera, "#seen")
	assert.NoError(t, err)
	_, _, err = db.JoinChannel(ctx, oftc, "#other", "", "")
	assert.NoError(t, err)

	// Only the channels it was in are returned, in order.
	left, err := db.DisconnectBackend(ctx, libera)
	assert.NoError(t, err)
	assert.Equal(t, []string{a.ID, b.ID}, channelIDs(left))
	for _, channel := range left {
		assert.False(t, channel.Joined)
	}

	backend, err := db.GetBackend(ctx, libera)
	assert.NoError(t, err)
	assert.False(t, backend.Connected)

	joined, err := db.ListChannels(ctx, "", false)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(joined), "the other backend's channel is still joined")

	left, err = db.DisconnectBackend(ctx, libera)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(left))
}

func TestResetConnections(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := connectTestBackend(t, db, "libera")
	_, _, err := db.JoinChannel(ctx, libera, "#chan", "", "")
	assert.NoError(t, err)

	assert.NoError(t, db.ResetConnections(ctx))

	backends, err := db.ListBackends(ctx, false)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(backends))
	channels, err := db.ListChannels(ctx, "", false)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(channels))
}

func TestChannelWrites(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := connectTestBackend(t, db, "libera")
	oftc := connectTestBackend(t, db, "oftc")

	// check takes a write's results, checks whether it reported a change, and
	// returns the channel it left.
	check := func(what string, wantChanged bool) func(*Channel, bool, error) *Channel {
		return func(channel *Channel, changed bool, err error) *Channel {
			t.Helper()
			assert.NoError(t, err, what)
			assert.Equal(t, wantChanged, changed, what)
			return channel
		}
	}

	joined := check("first join", true)(db.JoinChannel(ctx, libera, "#chan", "Chan", "hi"))
	assertSeabirdID(t, channelIDPrefix, joined.ID)
	assert.Equal(t, Channel{
		ID: joined.ID, BackendID: libera, ExternalID: "#chan", DisplayName: "Chan", Topic: "hi", Joined: true,
		FirstSeen: joined.FirstSeen, LastSeen: joined.LastSeen,
	}, *joined)

	check("same join again", false)(db.JoinChannel(ctx, libera, "#chan", "Chan", "hi"))
	again := check("join without a name", false)(db.JoinChannel(ctx, libera, "#chan", "", "hi"))
	assert.Equal(t, "Chan", again.DisplayName, "an empty name keeps the old one")

	changed := check("rename", true)(db.ChangeChannel(ctx, libera, "#chan", "Renamed", "bye"))
	assert.Equal(t, "Renamed", changed.DisplayName)
	assert.Equal(t, "bye", changed.Topic)
	assert.True(t, changed.Joined, "changing a channel doesn't leave it")
	check("same change again", false)(db.ChangeChannel(ctx, libera, "#chan", "Renamed", "bye"))
	check("message from a known channel", false)(db.RecordChannel(ctx, libera, "#chan"))

	left := check("leave", true)(db.LeaveChannel(ctx, libera, "#chan"))
	assert.False(t, left.Joined)
	assert.Equal(t, joined.ID, left.ID, "a channel keeps its ID")
	check("leave again", false)(db.LeaveChannel(ctx, libera, "#chan"))

	unknown, changedUnknown, err := db.LeaveChannel(ctx, libera, "#unknown")
	assert.NoError(t, err)
	assert.False(t, changedUnknown)
	assert.Zero(t, unknown, "leaving an unknown channel doesn't create it")

	seen := check("message from a new channel", true)(db.RecordChannel(ctx, libera, "#new"))
	assert.False(t, seen.Joined)
	assert.Equal(t, "", seen.DisplayName)

	other := check("same name on another backend", true)(db.JoinChannel(ctx, oftc, "#chan", "Chan", ""))
	assert.NotEqual(t, joined.ID, other.ID)
}

func TestRecordUser(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := connectTestBackend(t, db, "libera")
	oftc := connectTestBackend(t, db, "oftc")

	user, err := db.RecordUser(ctx, libera, "alice", "Alice")
	assert.NoError(t, err)
	assertSeabirdID(t, userIDPrefix, user.ID)
	assert.Equal(t, User{
		ID: user.ID, BackendID: libera, ExternalID: "alice", DisplayName: "Alice",
		FirstSeen: user.FirstSeen, LastSeen: user.LastSeen,
	}, *user)
	assert.NotZero(t, user.LastSeen)

	again, err := db.RecordUser(ctx, libera, "alice", "")
	assert.NoError(t, err)
	assert.Equal(t, user.ID, again.ID)
	assert.Equal(t, "Alice", again.DisplayName)

	other, err := db.RecordUser(ctx, oftc, "alice", "Alice")
	assert.NoError(t, err)
	assert.NotEqual(t, user.ID, other.ID, "the same name on another backend is another user")
}

func TestGetByID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := connectTestBackend(t, db, "libera")
	channel, _, err := db.JoinChannel(ctx, libera, "#chan", "", "")
	assert.NoError(t, err)
	user, err := db.RecordUser(ctx, libera, "alice", "")
	assert.NoError(t, err)

	backend, err := db.GetBackend(ctx, libera)
	assert.NoError(t, err)
	assert.Equal(t, libera, backend.ID)
	gotChannel, err := db.GetChannel(ctx, channel.ID)
	assert.NoError(t, err)
	assert.Equal(t, *channel, *gotChannel)
	gotUser, err := db.GetUser(ctx, user.ID)
	assert.NoError(t, err)
	assert.Equal(t, *user, *gotUser)

	_, err = db.GetBackend(ctx, "sbb_missing")
	assert.IsError(t, err, ErrNotFound)
	_, err = db.GetChannel(ctx, "sbc_missing")
	assert.IsError(t, err, ErrNotFound)
	_, err = db.GetUser(ctx, "sbu_missing")
	assert.IsError(t, err, ErrNotFound)
}

func TestListFilters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := connectTestBackend(t, db, "libera")
	oftc := connectTestBackend(t, db, "oftc")
	joined, _, err := db.JoinChannel(ctx, libera, "#joined", "", "")
	assert.NoError(t, err)
	seen, _, err := db.RecordChannel(ctx, libera, "#seen")
	assert.NoError(t, err)
	other, _, err := db.JoinChannel(ctx, oftc, "#other", "", "")
	assert.NoError(t, err)
	_, err = db.DisconnectBackend(ctx, oftc)
	assert.NoError(t, err)

	connected, err := db.ListBackends(ctx, false)
	assert.NoError(t, err)
	assert.Equal(t, []string{libera}, backendIDs(connected))
	all, err := db.ListBackends(ctx, true)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(all))

	channels, err := db.ListChannels(ctx, libera, false)
	assert.NoError(t, err)
	assert.Equal(t, []string{joined.ID}, channelIDs(channels))

	channels, err = db.ListChannels(ctx, libera, true)
	assert.NoError(t, err)
	assert.Equal(t, []string{joined.ID, seen.ID}, channelIDs(channels))

	channels, err = db.ListChannels(ctx, "", true)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(channels))
	assert.SliceContains(t, channelIDs(channels), other.ID)
}

func TestInsertWithNewIDRetriesTakenIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	taken := connectTestBackend(t, db, "libera")

	// Each connection hands out the taken ID collisions times before a fresh
	// one.
	connect := func(name string, collisions int) (int, error) {
		attempts := 0
		db.newID = func(prefix string) string {
			attempts++
			if attempts <= collisions {
				return taken
			}
			return newSeabirdID(prefix)
		}
		_, err := db.ConnectBackend(ctx, "irc", name, "")
		return attempts, err
	}

	attempts, err := connect("oftc", maxIDAttempts-1)
	assert.NoError(t, err)
	assert.Equal(t, maxIDAttempts, attempts)

	attempts, err = connect("efnet", maxIDAttempts)
	assert.Error(t, err)
	assert.Equal(t, maxIDAttempts, attempts)
}

var seabirdIDSuffix = regexp.MustCompile(`^[0-9abcdefghjkmnpqrstvwxyz]{13}$`)

// assertSeabirdID checks that id has the given prefix and 13 random
// characters.
func assertSeabirdID(t *testing.T, prefix, id string) {
	t.Helper()

	suffix, ok := strings.CutPrefix(id, prefix+"_")
	assert.True(t, ok, "%q doesn't start with %s_", id, prefix)
	assert.True(t, seabirdIDSuffix.MatchString(suffix), "%q isn't 13 Crockford characters", suffix)
}

// connectTestBackend connects an IRC backend and returns its ID.
func connectTestBackend(t *testing.T, db *DB, name string) string {
	t.Helper()

	backend, err := db.ConnectBackend(context.Background(), "irc", name, "")
	assert.NoError(t, err)
	return backend.ID
}

func backendIDs(backends []Backend) []string {
	ids := make([]string, 0, len(backends))
	for _, backend := range backends {
		ids = append(ids, backend.ID)
	}
	return ids
}

func channelIDs(channels []Channel) []string {
	ids := make([]string, 0, len(channels))
	for _, channel := range channels {
		ids = append(ids, channel.ID)
	}
	return ids
}
