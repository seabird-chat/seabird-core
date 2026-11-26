-- Track which backends are connected and which channels they're in. Both are
-- cleared when core starts, since backends announce them again on reconnect.
ALTER TABLE seabird_backends ADD COLUMN connected BOOLEAN NOT NULL DEFAULT 0;

ALTER TABLE seabird_channels ADD COLUMN joined BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE seabird_channels ADD COLUMN topic TEXT NOT NULL DEFAULT '';
