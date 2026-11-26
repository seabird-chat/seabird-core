package core

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestOpenDB(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "seabird.db")
	logger := slog.New(slog.DiscardHandler)

	db, err := OpenDB(ctx, dsn, logger)
	assert.NoError(t, err)
	insertToken(t, db, "plugin", "token")
	assert.NoError(t, db.Close())

	// Reopening an existing database keeps its data.
	db, err = OpenDB(ctx, dsn, logger)
	assert.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	token, err := db.GetAuthToken(ctx, "token")
	assert.NoError(t, err)
	assert.Equal(t, "plugin", token.Name)
}

func TestOpenDBInMemory(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	insertToken(t, db, "plugin", "token")

	// Every connection to :memory: is a separate database, so lookups made at
	// the same time must still share one.
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, err := db.GetAuthToken(context.Background(), "token")
			assert.NoError(t, err)
		})
	}
	wg.Wait()
}

func TestOpenDBFileURI(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "seabird test.db")
	dsn := "file:" + strings.ReplaceAll(path, " ", "%20") + "?mode=rwc"

	db, err := OpenDB(context.Background(), dsn, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	// The URI is decoded and its options aren't part of the filename.
	_, err = os.Stat(path)
	assert.NoError(t, err, "database file")
}

// Not parallel: it changes directory to check nothing is created there.
func TestOpenDBRejectsSQLiteURLs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	for _, dsn := range []string{"sqlite:seabird.db", "sqlite:///var/lib/seabird.db"} {
		_, err := OpenDB(context.Background(), dsn, slog.New(slog.DiscardHandler))
		assert.EqualError(t, err, "sqlite: URLs aren't supported; use a filename or a file: URI", dsn)
	}

	entries, err := os.ReadDir(dir)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(entries))
}

func TestGetAuthToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	insertToken(t, db, "plugin", "plugin-key")
	insertToken(t, db, "backend", "backend-key")

	token, err := db.GetAuthToken(ctx, "backend-key")
	assert.NoError(t, err)
	assert.Equal(t, "backend", token.Name)

	// Keys are matched exactly.
	for _, key := range []string{"", "PLUGIN-KEY", "plugin-key ", "plugin"} {
		_, err := db.GetAuthToken(ctx, key)
		assert.IsError(t, err, ErrNotFound, "key %q", key)
	}
}

func TestRecordBackend(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)

	backend, err := db.RecordBackend(ctx, "irc", "libera")
	assert.NoError(t, err)
	assertSeabirdID(t, backendIDPrefix, backend.ID)
	assert.Equal(t, "irc", backend.Type)
	assert.Equal(t, "libera", backend.NativeID)
	assert.NotZero(t, backend.LastSeen)

	again, err := db.RecordBackend(ctx, "irc", "libera")
	assert.NoError(t, err)
	assert.Equal(t, backend.ID, again.ID, "a backend keeps its ID")

	other, err := db.RecordBackend(ctx, "discord", "libera")
	assert.NoError(t, err)
	assert.NotEqual(t, backend.ID, other.ID, "backends of different types are different backends")
}

func TestRecordChannel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := recordTestBackend(t, db, "libera")
	oftc := recordTestBackend(t, db, "oftc")

	channel, err := db.RecordChannel(ctx, libera, "#chan", "Chan")
	assert.NoError(t, err)
	assertSeabirdID(t, channelIDPrefix, channel.ID)
	assert.Equal(t, Channel{
		ID: channel.ID, BackendID: libera, NativeID: "#chan", DisplayName: "Chan",
		FirstSeen: channel.FirstSeen, LastSeen: channel.LastSeen,
	}, *channel)
	assert.NotZero(t, channel.LastSeen)

	// Seeing it again without a name keeps both the ID and the name.
	again, err := db.RecordChannel(ctx, libera, "#chan", "")
	assert.NoError(t, err)
	assert.Equal(t, channel.ID, again.ID)
	assert.Equal(t, "Chan", again.DisplayName)

	renamed, err := db.RecordChannel(ctx, libera, "#chan", "Renamed")
	assert.NoError(t, err)
	assert.Equal(t, "Renamed", renamed.DisplayName)

	other, err := db.RecordChannel(ctx, oftc, "#chan", "Chan")
	assert.NoError(t, err)
	assert.NotEqual(t, channel.ID, other.ID, "the same name on another backend is another channel")
}

func TestRecordUser(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := recordTestBackend(t, db, "libera")
	oftc := recordTestBackend(t, db, "oftc")

	user, err := db.RecordUser(ctx, libera, "alice", "Alice")
	assert.NoError(t, err)
	assertSeabirdID(t, userIDPrefix, user.ID)
	assert.Equal(t, User{
		ID: user.ID, BackendID: libera, NativeID: "alice", DisplayName: "Alice",
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

func TestListRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	libera := recordTestBackend(t, db, "libera")
	channel, err := db.RecordChannel(ctx, libera, "#chan", "Chan")
	assert.NoError(t, err)
	user, err := db.RecordUser(ctx, libera, "alice", "Alice")
	assert.NoError(t, err)

	backends, err := db.ListBackends(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(backends))
	assert.Equal(t, libera, backends[0].ID)

	channels, err := db.ListChannels(ctx)
	assert.NoError(t, err)
	assert.Equal(t, []Channel{*channel}, channels)

	users, err := db.ListUsers(ctx)
	assert.NoError(t, err)
	assert.Equal(t, []User{*user}, users)
}

func TestInsertWithNewIDRetriesTakenIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestDB(t)
	taken := recordTestBackend(t, db, "libera")

	// Each seed hands out the taken ID collisions times before a fresh one.
	seed := func(nativeID string, collisions int) (int, error) {
		attempts := 0
		db.newID = func(prefix string) string {
			attempts++
			if attempts <= collisions {
				return taken
			}
			return newSeabirdID(prefix)
		}
		_, err := db.RecordBackend(ctx, "irc", nativeID)
		return attempts, err
	}

	attempts, err := seed("oftc", maxIDAttempts-1)
	assert.NoError(t, err)
	assert.Equal(t, maxIDAttempts, attempts)

	attempts, err = seed("efnet", maxIDAttempts)
	assert.Error(t, err)
	assert.Equal(t, maxIDAttempts, attempts)
}

var seabirdIDSuffix = regexp.MustCompile(`^[0-9abcdefghjkmnpqrstvwxyz]{13}$`)

// assertSeabirdID checks that id is a seabird ID with the given prefix.
func assertSeabirdID(t *testing.T, prefix, id string) {
	t.Helper()

	suffix, ok := strings.CutPrefix(id, prefix+"_")
	assert.True(t, ok, "%q doesn't start with %s_", id, prefix)
	assert.True(t, seabirdIDSuffix.MatchString(suffix), "%q isn't 13 Crockford characters", suffix)
}

// recordTestBackend records an IRC backend and returns its ID.
func recordTestBackend(t *testing.T, db *DB, nativeID string) string {
	t.Helper()

	backend, err := db.RecordBackend(context.Background(), "irc", nativeID)
	assert.NoError(t, err)
	return backend.ID
}
