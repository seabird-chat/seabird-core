package core

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/belak/x/migrate"
	"github.com/belak/x/slogx"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// AuthToken is a row of seabird_auth_tokens. Name doubles as the username
// requests are attributed to.
type AuthToken struct {
	ID   int64
	Name string
	Key  string
}

// DB holds the sqlite connection pool used for auth token lookups.
type DB struct {
	inner *sql.DB
}

// OpenDB opens the database named by a sqlx-style DATABASE_URL and applies any
// pending migrations.
func OpenDB(ctx context.Context, databaseURL string, logger *slog.Logger) (*DB, error) {
	dsn, err := sqliteDSN(databaseURL)
	if err != nil {
		return nil, err
	}

	inner, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// sqlite only supports a single writer, so extra connections buy us
	// nothing but lock contention.
	inner.SetMaxOpenConns(1)

	if err := inner.PingContext(ctx); err != nil {
		inner.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	m := migrate.New(
		migrate.NewDriver(inner, migrate.SQLiteDialect{}),
		migrate.WithLayers(migrate.Layer{Name: "core", FS: migrationsFS}),
	)

	result, err := m.Migrate(ctx)
	if err != nil {
		inner.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	if len(result.Applied) > 0 {
		logger.Info("applied migrations", slogx.Any("versions", result.Applied))
	}

	return &DB{inner: inner}, nil
}

// Close releases the connection pool.
func (d *DB) Close() error {
	return d.inner.Close()
}

// GetAuthToken looks up a token by key. An unknown key returns a nil token and
// a nil error.
func (d *DB) GetAuthToken(ctx context.Context, key string) (*AuthToken, error) {
	var token AuthToken

	err := d.inner.QueryRowContext(ctx,
		"SELECT id, name, key FROM seabird_auth_tokens WHERE key = ? LIMIT 1",
		key,
	).Scan(&token.ID, &token.Name, &token.Key)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("querying auth token: %w", err)
	}

	return &token, nil
}

// sqliteDSN converts DATABASE_URL into a path the sqlite driver understands.
// Existing deployments are configured with sqlx URLs such as
// "sqlite:///var/lib/seabird-core/seabird-core.db" and "sqlite:seabird.db", so
// every form sqlx accepted still has to work.
func sqliteDSN(databaseURL string) (string, error) {
	if databaseURL == "" {
		return "", errors.New("DATABASE_URL is empty")
	}

	rest, ok := strings.CutPrefix(databaseURL, "sqlite:")
	if !ok {
		// A bare filesystem path is also valid.
		return databaseURL, nil
	}

	rest = strings.TrimPrefix(rest, "//")
	if rest == "" {
		return "", fmt.Errorf("DATABASE_URL %q has no path", databaseURL)
	}

	return rest, nil
}
