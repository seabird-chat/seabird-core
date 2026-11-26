-- Create channel groups, which plugins see as a single channel made up of
-- their members. A channel is in at most one group.
CREATE TABLE seabird_channel_groups (
  id TEXT PRIMARY KEY NOT NULL,
  name TEXT NOT NULL,
  CONSTRAINT channel_group_name_unique UNIQUE (name)
);

ALTER TABLE seabird_channels ADD COLUMN group_id TEXT REFERENCES seabird_channel_groups (id);
