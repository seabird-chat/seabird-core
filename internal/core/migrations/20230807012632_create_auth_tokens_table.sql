-- IF NOT EXISTS is load-bearing: existing deployments already have this table
-- from the sqlx-managed schema, which tracked applied migrations in a
-- different table than this migrator uses.
CREATE TABLE IF NOT EXISTS seabird_auth_tokens (
  id INTEGER PRIMARY KEY NOT NULL,
  name TEXT NOT NULL,
  key TEXT NOT NULL,
  CONSTRAINT key_unique UNIQUE (key)
);
