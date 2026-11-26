package core

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/belak/x/migrate"
	"github.com/belak/x/slogx"
	"github.com/georgysavva/scany/v2/sqlscan"
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

	return &DB{inner: inner}, nil
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
