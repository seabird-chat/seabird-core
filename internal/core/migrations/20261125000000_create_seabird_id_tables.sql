-- Create the tables that give backends, channels and users seabird IDs. Rows
-- created ahead of time have no last_seen until a backend reports them.
CREATE TABLE seabird_backends (
  id TEXT PRIMARY KEY NOT NULL,
  type TEXT NOT NULL,
  native_id TEXT NOT NULL,
  first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen DATETIME,
  CONSTRAINT backend_unique UNIQUE (type, native_id)
);

CREATE TABLE seabird_channels (
  id TEXT PRIMARY KEY NOT NULL,
  backend_id TEXT NOT NULL REFERENCES seabird_backends (id),
  native_id TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen DATETIME,
  CONSTRAINT channel_unique UNIQUE (backend_id, native_id)
);

CREATE TABLE seabird_users (
  id TEXT PRIMARY KEY NOT NULL,
  backend_id TEXT NOT NULL REFERENCES seabird_backends (id),
  native_id TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen DATETIME,
  CONSTRAINT user_unique UNIQUE (backend_id, native_id)
);
