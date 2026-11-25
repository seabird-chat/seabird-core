-- Create the authentication token table.
CREATE TABLE seabird_auth_tokens (
  id INTEGER PRIMARY KEY NOT NULL,
  name TEXT NOT NULL,
  key TEXT NOT NULL,
  CONSTRAINT key_unique UNIQUE (key)
);
