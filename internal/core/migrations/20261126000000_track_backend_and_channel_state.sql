-- Track which backends are connected and which channels they're in. Both are
-- cleared when core starts, since backends announce them again on reconnect.
-- Also rename native_id to say what it holds in each table.
ALTER TABLE seabird_backends ADD COLUMN connected BOOLEAN NOT NULL DEFAULT 0;

ALTER TABLE seabird_channels ADD COLUMN joined BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE seabird_channels ADD COLUMN topic TEXT NOT NULL DEFAULT '';

ALTER TABLE seabird_backends RENAME COLUMN native_id TO name;
ALTER TABLE seabird_channels RENAME COLUMN native_id TO external_id;
ALTER TABLE seabird_users RENAME COLUMN native_id TO external_id;
