package core

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/belak/x/migrate"
	"github.com/belak/x/slogx"
	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound indicates that a lookup matched no rows.
var ErrNotFound = errors.New("not found")

// AuthToken is a row of seabird_auth_tokens. Name is the username requests
// made with the token are attributed to.
type AuthToken struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

// DB owns the connection pool used by core.
type DB struct {
	inner *sql.DB

	// newID generates seabird IDs. Tests replace it to force collisions.
	newID func(prefix string) string
}

// OpenDB opens SQLite and applies the core migrations. dsn is a filename or a
// file: URI, which is where driver options go.
func OpenDB(ctx context.Context, dsn string, logger *slogx.Logger) (*DB, error) {
	// The driver would take a sqlite: URL as a literal filename.
	if strings.HasPrefix(dsn, "sqlite:") {
		return nil, errors.New("sqlite: URLs aren't supported; use a filename or a file: URI")
	}

	inner, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Without this, the connection pool will create a new :memory: DB for each
	// connection, leading to only one of them having the migrations run.
	if dsn == ":memory:" {
		inner.SetMaxOpenConns(1)
	}

	// sql.Open doesn't connect, so check that the database can be opened.
	if err := inner.PingContext(ctx); err != nil {
		inner.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	migrator := migrate.New(
		migrate.NewDriver(inner, migrate.SQLiteDialect{}),
		migrate.WithDirectory("migrations"),
		migrate.WithLayers(migrate.Layer{Name: "seabird-core", FS: migrations}),
	)

	result, err := migrator.Migrate(ctx)
	if err != nil {
		inner.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	if len(result.Applied) > 0 {
		logger.InfoContext(ctx, "applied migrations", slogx.Any("versions", result.Applied))
	} else {
		logger.DebugContext(ctx, "database migrations are up to date")
	}

	return &DB{inner: inner, newID: newSeabirdID}, nil
}

// Close closes the database connection pool.
func (d *DB) Close() error {
	return d.inner.Close()
}

// GetAuthToken looks up a token by its key. It returns ErrNotFound if no token
// matches.
func (d *DB) GetAuthToken(ctx context.Context, key string) (*AuthToken, error) {
	var token AuthToken
	err := sqlscan.Get(ctx, d.inner, &token, "SELECT id, name FROM seabird_auth_tokens WHERE key = ?", key)
	if sqlscan.NotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query auth token: %w", err)
	}
	return &token, nil
}

// Backend is a chat backend core has seen or been told about, as stored in
// seabird_backends. Backends identify themselves by Type and NativeID, the
// type and ID in their hello.
type Backend struct {
	ID        string     `db:"id"`
	Type      string     `db:"type"`
	NativeID  string     `db:"native_id"`
	FirstSeen time.Time  `db:"first_seen"`
	LastSeen  *time.Time `db:"last_seen"` // nil if it has never connected
}

// Channel is a channel on a backend, as stored in seabird_channels. NativeID
// is what the backend calls it.
type Channel struct {
	ID          string     `db:"id"`
	BackendID   string     `db:"backend_id"`
	NativeID    string     `db:"native_id"`
	DisplayName string     `db:"display_name"`
	FirstSeen   time.Time  `db:"first_seen"`
	LastSeen    *time.Time `db:"last_seen"` // nil if the backend has never reported it
}

// User is a user on a backend, as stored in seabird_users. NativeID is what
// the backend calls them.
type User struct {
	ID          string     `db:"id"`
	BackendID   string     `db:"backend_id"`
	NativeID    string     `db:"native_id"`
	DisplayName string     `db:"display_name"`
	FirstSeen   time.Time  `db:"first_seen"`
	LastSeen    *time.Time `db:"last_seen"` // nil if the backend has never reported them
}

// RecordBackend notes that a backend connected, creating it the first time it
// is seen.
func (d *DB) RecordBackend(ctx context.Context, backendType, nativeID string) (*Backend, error) {
	return insertWithNewID[Backend](ctx, d, backendIDPrefix, `
		INSERT INTO seabird_backends (id, type, native_id, last_seen)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (type, native_id) DO UPDATE SET last_seen = CURRENT_TIMESTAMP
		RETURNING *`,
		backendType, nativeID)
}

// RecordChannel notes that a backend reported a channel, creating it the first
// time it is seen. An empty displayName keeps the one already known.
func (d *DB) RecordChannel(ctx context.Context, backendID, nativeID, displayName string) (*Channel, error) {
	return insertWithNewID[Channel](ctx, d, channelIDPrefix, `
		INSERT INTO seabird_channels (id, backend_id, native_id, display_name, last_seen)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (backend_id, native_id) DO UPDATE SET
			display_name = coalesce(nullif(excluded.display_name, ''), display_name),
			last_seen = CURRENT_TIMESTAMP
		RETURNING *`,
		backendID, nativeID, displayName)
}

// RecordUser notes that a backend reported a user, creating them the first
// time they are seen. An empty displayName keeps the one already known.
func (d *DB) RecordUser(ctx context.Context, backendID, nativeID, displayName string) (*User, error) {
	return insertWithNewID[User](ctx, d, userIDPrefix, `
		INSERT INTO seabird_users (id, backend_id, native_id, display_name, last_seen)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (backend_id, native_id) DO UPDATE SET
			display_name = coalesce(nullif(excluded.display_name, ''), display_name),
			last_seen = CURRENT_TIMESTAMP
		RETURNING *`,
		backendID, nativeID, displayName)
}

// ListBackends returns every backend, ordered by type and native ID.
func (d *DB) ListBackends(ctx context.Context) ([]Backend, error) {
	var backends []Backend
	err := sqlscan.Select(ctx, d.inner, &backends, "SELECT * FROM seabird_backends ORDER BY type, native_id")
	if err != nil {
		return nil, fmt.Errorf("query backends: %w", err)
	}
	return backends, nil
}

// ListChannels returns every channel, ordered by backend and native ID.
func (d *DB) ListChannels(ctx context.Context) ([]Channel, error) {
	var channels []Channel
	err := sqlscan.Select(ctx, d.inner, &channels, "SELECT * FROM seabird_channels ORDER BY backend_id, native_id")
	if err != nil {
		return nil, fmt.Errorf("query channels: %w", err)
	}
	return channels, nil
}

// ListUsers returns every user, ordered by backend and native ID.
func (d *DB) ListUsers(ctx context.Context) ([]User, error) {
	var users []User
	err := sqlscan.Select(ctx, d.inner, &users, "SELECT * FROM seabird_users ORDER BY backend_id, native_id")
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	return users, nil
}

// maxIDAttempts is how many new IDs to try before giving up on a row. A
// collision needs two random IDs to match, so a second try is already rare.
const maxIDAttempts = 3

// insertWithNewID runs query, which takes a new ID followed by args and
// returns one row, trying another ID if that one is taken.
func insertWithNewID[T any](ctx context.Context, d *DB, prefix, query string, args ...any) (*T, error) {
	for attempt := 1; ; attempt++ {
		var row T
		err := sqlscan.Get(ctx, d.inner, &row, query, append([]any{d.newID(prefix)}, args...)...)
		if errors.Is(err, sqlite3.CONSTRAINT_PRIMARYKEY) && attempt < maxIDAttempts {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("insert %s: %w", prefix, err)
		}
		return &row, nil
	}
}
