package core

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestSQLiteDSN(t *testing.T) {
	tests := []struct {
		name        string
		databaseURL string
		want        string
		wantErr     bool
	}{
		{"absolute url", "sqlite:///var/lib/seabird-core/core.db", "/var/lib/seabird-core/core.db", false},
		{"relative url", "sqlite://core.db", "core.db", false},
		{"no slashes", "sqlite:core.db", "core.db", false},
		{"bare path", "/var/lib/core.db", "/var/lib/core.db", false},
		{"empty", "", "", true},
		{"scheme only", "sqlite://", "", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := sqliteDSN(test.databaseURL)
			if test.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestGetAuthToken(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	url := "sqlite://" + filepath.Join(t.TempDir(), "core.db")

	db, err := OpenDB(ctx, url, logger)
	assert.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	// Migrations are idempotent, so reopening an existing database has to
	// work: deployments upgrading from the sqlx-managed schema depend on it.
	again, err := OpenDB(ctx, url, logger)
	assert.NoError(t, err)
	assert.NoError(t, again.Close())

	_, err = db.inner.ExecContext(ctx,
		"INSERT INTO seabird_auth_tokens (name, key) VALUES (?, ?)", "plugin", "secret")
	assert.NoError(t, err)

	token, err := db.GetAuthToken(ctx, "secret")
	assert.NoError(t, err)
	assert.NotZero(t, token)
	assert.Equal(t, "plugin", token.Name)

	missing, err := db.GetAuthToken(ctx, "nope")
	assert.NoError(t, err)
	assert.Zero(t, missing)
}
