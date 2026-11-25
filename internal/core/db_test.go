package core

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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
