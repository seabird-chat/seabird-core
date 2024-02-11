CREATE TABLE seabird_auth_tokens (
  id INTEGER PRIMARY KEY NOT NULL,
  name TEXT NOT NULL,
  key TEXT NOT NULL,
  status TEXT NOT NULL,
  CONSTRAINT key_unique UNIQUE (key)
);
