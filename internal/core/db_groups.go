package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/ncruces/go-sqlite3"
)

// ChannelGroup is a set of channels plugins see as one, as stored in
// seabird_channel_groups. Its members are the channels whose GroupID is its ID.
type ChannelGroup struct {
	ID   string `db:"id"`
	Name string `db:"name"`
}

// ErrGroupExists is returned when creating a group with a name that's taken.
var ErrGroupExists = errors.New("group already exists")

// CreateChannelGroup adds an empty group named name.
func (d *DB) CreateChannelGroup(ctx context.Context, name string) (*ChannelGroup, error) {
	if name == "" {
		return nil, errors.New("group name must not be empty")
	}

	group, err := insertWithNewID[ChannelGroup](ctx, d.inner, d.newID, groupIDPrefix,
		"INSERT INTO seabird_channel_groups (id, name) VALUES (?, ?) RETURNING *", name)
	if errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		return nil, fmt.Errorf("%w: %q", ErrGroupExists, name)
	}
	return group, err
}

// GetChannelGroup returns the group with the given ID, or ErrNotFound.
func (d *DB) GetChannelGroup(ctx context.Context, id string) (*ChannelGroup, error) {
	return getByID[ChannelGroup](ctx, d, "SELECT * FROM seabird_channel_groups WHERE id = ?", id)
}

// GetChannelGroupByName returns the group with the given name, or ErrNotFound.
func (d *DB) GetChannelGroupByName(ctx context.Context, name string) (*ChannelGroup, error) {
	return getByID[ChannelGroup](ctx, d, "SELECT * FROM seabird_channel_groups WHERE name = ?", name)
}

// ListChannelGroups returns every group, ordered by name.
func (d *DB) ListChannelGroups(ctx context.Context) ([]ChannelGroup, error) {
	var groups []ChannelGroup
	err := sqlscan.Select(ctx, d.inner, &groups, "SELECT * FROM seabird_channel_groups ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("query channel groups: %w", err)
	}
	return groups, nil
}

// ListGroupMembers returns the channels in a group, ordered by backend and
// external ID.
func (d *DB) ListGroupMembers(ctx context.Context, groupID string) ([]Channel, error) {
	var channels []Channel
	err := sqlscan.Select(ctx, d.inner, &channels,
		"SELECT * FROM seabird_channels WHERE group_id = ? ORDER BY backend_id, external_id", groupID)
	if err != nil {
		return nil, fmt.Errorf("query group members: %w", err)
	}
	return channels, nil
}

// AddToChannelGroup puts channels in a group, all or none. It fails if any
// channel is unknown or already in another group. Adding a channel that's
// already in this group does nothing.
func (d *DB) AddToChannelGroup(ctx context.Context, groupID string, channelIDs ...string) error {
	return d.updateGroupMembers(ctx, groupID, channelIDs, func(channel Channel) (*string, error) {
		if channel.GroupID != nil && *channel.GroupID != groupID {
			return nil, fmt.Errorf("channel %s is already in group %s", channel.ID, *channel.GroupID)
		}
		return &groupID, nil
	})
}

// RemoveFromChannelGroup takes channels out of a group, all or none. It fails
// if any channel is unknown or isn't in the group.
func (d *DB) RemoveFromChannelGroup(ctx context.Context, groupID string, channelIDs ...string) error {
	return d.updateGroupMembers(ctx, groupID, channelIDs, func(channel Channel) (*string, error) {
		if channel.GroupID == nil || *channel.GroupID != groupID {
			return nil, fmt.Errorf("channel %s isn't in group %s", channel.ID, groupID)
		}
		return nil, nil
	})
}

// DeleteChannelGroup removes a group, leaving its members ungrouped. It
// returns ErrNotFound if there is none.
func (d *DB) DeleteChannelGroup(ctx context.Context, groupID string) error {
	tx, err := d.inner.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "UPDATE seabird_channels SET group_id = NULL WHERE group_id = ?", groupID); err != nil {
		return fmt.Errorf("ungroup members: %w", err)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM seabird_channel_groups WHERE id = ?", groupID)
	if err != nil {
		return fmt.Errorf("delete channel group: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete channel group: %w", err)
	}
	if deleted == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// updateGroupMembers sets each channel's group to what next returns for it,
// all or none. The group must exist.
func (d *DB) updateGroupMembers(
	ctx context.Context, groupID string, channelIDs []string, next func(Channel) (*string, error),
) error {
	tx, err := d.inner.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	var group ChannelGroup
	err = sqlscan.Get(ctx, tx, &group, "SELECT * FROM seabird_channel_groups WHERE id = ?", groupID)
	if sqlscan.NotFound(err) {
		return fmt.Errorf("group %s: %w", groupID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("look up group: %w", err)
	}

	for _, id := range channelIDs {
		var channel Channel
		err := sqlscan.Get(ctx, tx, &channel, "SELECT * FROM seabird_channels WHERE id = ?", id)
		if sqlscan.NotFound(err) {
			return fmt.Errorf("channel %s: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("look up channel: %w", err)
		}

		group, err := next(channel)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE seabird_channels SET group_id = ? WHERE id = ?", group, id); err != nil {
			return fmt.Errorf("update channel: %w", err)
		}
	}
	return tx.Commit()
}
