CREATE TABLE seabird_channel_links (
  id INTEGER PRIMARY KEY NOT NULL,
  auth_token_id INT NOT NULL,
  channel_id TEXT NOT NULL,
  name TEXT NOT NULL,
  status TEXT NOT NULL,
  FOREIGN KEY (auth_token_id) REFERENCES seabird_auth_tokens(id),
  FOREIGN KEY (channel_id) REFERENCES seabird_channels(uuid),
  UNIQUE (auth_token_id, name)
)
