package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/ncruces/go-sqlite3"
)

// Backend is a chat backend that is or was connected to core, as stored in
// seabird_backends. Type and Name are the type and name from its hello.
type Backend struct {
	ID        string     `db:"id"`
	Type      string     `db:"type"`
	Name      string     `db:"name"`
	FirstSeen time.Time  `db:"first_seen"`
	LastSeen  *time.Time `db:"last_seen"` // nil if it has never connected
	Connected bool       `db:"connected"`
}

// Channel is a channel on a backend, as stored in seabird_channels. ExternalID
// is what the backend calls it.
type Channel struct {
	ID          string     `db:"id"`
	BackendID   string     `db:"backend_id"`
	ExternalID  string     `db:"external_id"`
	DisplayName string     `db:"display_name"`
	Topic       string     `db:"topic"`
	FirstSeen   time.Time  `db:"first_seen"`
	LastSeen    *time.Time `db:"last_seen"` // nil if the backend has never reported it
	Joined      bool       `db:"joined"`
	GroupID     *string    `db:"group_id"` // nil if it isn't in a group
}

// User is a user on a backend, as stored in seabird_users. ExternalID is what
// the backend calls them.
type User struct {
	ID          string     `db:"id"`
	BackendID   string     `db:"backend_id"`
	ExternalID  string     `db:"external_id"`
	DisplayName string     `db:"display_name"`
	FirstSeen   time.Time  `db:"first_seen"`
	LastSeen    *time.Time `db:"last_seen"` // nil if the backend has never reported them
}

// ResetConnections marks every backend as disconnected and every channel as
// not joined. Core calls it on startup, before any backend can connect.
func (d *DB) ResetConnections(ctx context.Context) error {
	tx, err := d.inner.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "UPDATE seabird_backends SET connected = 0 WHERE connected"); err != nil {
		return fmt.Errorf("reset backends: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE seabird_channels SET joined = 0 WHERE joined"); err != nil {
		return fmt.Errorf("reset channels: %w", err)
	}
	return tx.Commit()
}

// ConnectBackend marks a backend as connected, creating it the first time it
// is seen.
func (d *DB) ConnectBackend(ctx context.Context, backendType, name string) (*Backend, error) {
	return insertWithNewID[Backend](ctx, d.inner, d.newID, backendIDPrefix, `
		INSERT INTO seabird_backends (id, type, name, last_seen, connected)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, 1)
		ON CONFLICT (type, name) DO UPDATE SET last_seen = CURRENT_TIMESTAMP, connected = 1
		RETURNING *`,
		backendType, name)
}

// DisconnectBackend marks a backend as disconnected and its channels as not
// joined. It returns the channels that were joined, ordered by external ID.
func (d *DB) DisconnectBackend(ctx context.Context, backendID string) ([]Channel, error) {
	tx, err := d.inner.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "UPDATE seabird_backends SET connected = 0 WHERE id = ?", backendID); err != nil {
		return nil, fmt.Errorf("disconnect backend: %w", err)
	}

	var left []Channel
	err = sqlscan.Select(ctx, tx, &left,
		"UPDATE seabird_channels SET joined = 0 WHERE backend_id = ? AND joined RETURNING *", backendID)
	if err != nil {
		return nil, fmt.Errorf("leave channels: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	slices.SortFunc(left, func(a, b Channel) int { return strings.Compare(a.ExternalID, b.ExternalID) })
	return left, nil
}

// The channel writes below report whether the channel changed in a way plugins
// can see: it's new, or its joined state, display name, or topic changed.

// JoinChannel notes that a backend joined a channel, creating it the first time
// it is seen. An empty displayName keeps the one already known.
func (d *DB) JoinChannel(ctx context.Context, backendID, externalID, displayName, topic string) (*Channel, bool, error) {
	return d.writeChannel(ctx, backendID, externalID, true, `
		INSERT INTO seabird_channels (id, backend_id, external_id, display_name, topic, joined, last_seen)
		VALUES (?, ?, ?, ?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT (backend_id, external_id) DO UPDATE SET
			display_name = coalesce(nullif(excluded.display_name, ''), display_name),
			topic = excluded.topic,
			joined = 1,
			last_seen = CURRENT_TIMESTAMP
		RETURNING *`,
		displayName, topic)
}

// ChangeChannel updates a channel's display name and topic, creating the
// channel the first time it is seen. An empty displayName keeps the one
// already known.
func (d *DB) ChangeChannel(ctx context.Context, backendID, externalID, displayName, topic string) (*Channel, bool, error) {
	return d.writeChannel(ctx, backendID, externalID, true, `
		INSERT INTO seabird_channels (id, backend_id, external_id, display_name, topic, last_seen)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (backend_id, external_id) DO UPDATE SET
			display_name = coalesce(nullif(excluded.display_name, ''), display_name),
			topic = excluded.topic,
			last_seen = CURRENT_TIMESTAMP
		RETURNING *`,
		displayName, topic)
}

// RecordChannel notes that a backend reported a channel, such as by sending a
// message from it, creating it the first time it is seen.
func (d *DB) RecordChannel(ctx context.Context, backendID, externalID string) (*Channel, bool, error) {
	return d.writeChannel(ctx, backendID, externalID, true, `
		INSERT INTO seabird_channels (id, backend_id, external_id, last_seen)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (backend_id, external_id) DO UPDATE SET last_seen = CURRENT_TIMESTAMP
		RETURNING *`)
}

// LeaveChannel notes that a backend left a channel. A channel that has never
// been seen is ignored, and nil is returned for it.
func (d *DB) LeaveChannel(ctx context.Context, backendID, externalID string) (*Channel, bool, error) {
	return d.writeChannel(ctx, backendID, externalID, false, `
		UPDATE seabird_channels SET joined = 0, last_seen = CURRENT_TIMESTAMP
		WHERE backend_id = ? AND external_id = ?
		RETURNING *`)
}

// writeChannel runs write, which takes backendID, externalID and then args, and
// returns the channel and whether it changed. Writes that can create channels
// also take a new ID first. Unknown channels are left alone otherwise.
func (d *DB) writeChannel(
	ctx context.Context, backendID, externalID string, creates bool, write string, args ...any,
) (*Channel, bool, error) {
	tx, err := d.inner.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	var before Channel
	err = sqlscan.Get(ctx, tx, &before,
		"SELECT * FROM seabird_channels WHERE backend_id = ? AND external_id = ?", backendID, externalID)
	existed := err == nil
	if err != nil && !sqlscan.NotFound(err) {
		return nil, false, fmt.Errorf("look up channel: %w", err)
	}
	if !existed && !creates {
		return nil, false, nil
	}

	args = append([]any{backendID, externalID}, args...)
	var after *Channel
	if creates {
		after, err = insertWithNewID[Channel](ctx, tx, d.newID, channelIDPrefix, write, args...)
	} else {
		after = &Channel{}
		err = sqlscan.Get(ctx, tx, after, write, args...)
	}
	if err != nil {
		return nil, false, fmt.Errorf("write channel: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit: %w", err)
	}

	changed := !existed ||
		before.Joined != after.Joined ||
		before.DisplayName != after.DisplayName ||
		before.Topic != after.Topic
	return after, changed, nil
}

// RecordUser notes that a backend reported a user, creating them the first
// time they are seen. An empty displayName keeps the one already known.
func (d *DB) RecordUser(ctx context.Context, backendID, externalID, displayName string) (*User, error) {
	return insertWithNewID[User](ctx, d.inner, d.newID, userIDPrefix, `
		INSERT INTO seabird_users (id, backend_id, external_id, display_name, last_seen)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (backend_id, external_id) DO UPDATE SET
			display_name = coalesce(nullif(excluded.display_name, ''), display_name),
			last_seen = CURRENT_TIMESTAMP
		RETURNING *`,
		backendID, externalID, displayName)
}

// GetBackend returns the backend with the given ID, or ErrNotFound.
func (d *DB) GetBackend(ctx context.Context, id string) (*Backend, error) {
	return getByID[Backend](ctx, d, "SELECT * FROM seabird_backends WHERE id = ?", id)
}

// GetChannel returns the channel with the given ID, or ErrNotFound.
func (d *DB) GetChannel(ctx context.Context, id string) (*Channel, error) {
	return getByID[Channel](ctx, d, "SELECT * FROM seabird_channels WHERE id = ?", id)
}

// GetUser returns the user with the given ID, or ErrNotFound.
func (d *DB) GetUser(ctx context.Context, id string) (*User, error) {
	return getByID[User](ctx, d, "SELECT * FROM seabird_users WHERE id = ?", id)
}

// ListBackends returns the connected backends, or every backend if
// includeDisconnected is set, ordered by type and name.
func (d *DB) ListBackends(ctx context.Context, includeDisconnected bool) ([]Backend, error) {
	var backends []Backend
	err := sqlscan.Select(ctx, d.inner, &backends,
		"SELECT * FROM seabird_backends WHERE connected OR ? ORDER BY type, name", includeDisconnected)
	if err != nil {
		return nil, fmt.Errorf("query backends: %w", err)
	}
	return backends, nil
}

// ListChannels returns the joined channels on a backend, or on every backend
// if backendID is empty. includeInactive adds the channels that aren't
// joined. They are ordered by backend and external ID.
func (d *DB) ListChannels(ctx context.Context, backendID string, includeInactive bool) ([]Channel, error) {
	var channels []Channel
	err := sqlscan.Select(ctx, d.inner, &channels, `
		SELECT * FROM seabird_channels
		WHERE (? = '' OR backend_id = ?) AND (joined OR ?)
		ORDER BY backend_id, external_id`,
		backendID, backendID, includeInactive)
	if err != nil {
		return nil, fmt.Errorf("query channels: %w", err)
	}
	return channels, nil
}

// ListUsers returns every user, ordered by backend and external ID.
func (d *DB) ListUsers(ctx context.Context) ([]User, error) {
	var users []User
	err := sqlscan.Select(ctx, d.inner, &users, "SELECT * FROM seabird_users ORDER BY backend_id, external_id")
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	return users, nil
}

// getByID runs a query for one row by ID, returning ErrNotFound if there is
// none.
func getByID[T any](ctx context.Context, d *DB, query, id string) (*T, error) {
	var row T
	err := sqlscan.Get(ctx, d.inner, &row, query, id)
	if sqlscan.NotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", id, err)
	}
	return &row, nil
}

// maxIDAttempts is how many new IDs to try before giving up on a row. A
// collision needs two random IDs to match, so a second try is already rare.
const maxIDAttempts = 3

// insertWithNewID runs query, which takes a new ID followed by args and
// returns one row, trying another ID if that one is taken.
func insertWithNewID[T any](
	ctx context.Context, q sqlscan.Querier, newID func(string) string, prefix, query string, args ...any,
) (*T, error) {
	for attempt := 1; ; attempt++ {
		var row T
		err := sqlscan.Get(ctx, q, &row, query, append([]any{newID(prefix)}, args...)...)
		if errors.Is(err, sqlite3.CONSTRAINT_PRIMARYKEY) && attempt < maxIDAttempts {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("insert %s: %w", prefix, err)
		}
		return &row, nil
	}
}
