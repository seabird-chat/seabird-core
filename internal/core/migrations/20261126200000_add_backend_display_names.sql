-- Record how people refer to each backend, which it gives in its hello.
ALTER TABLE seabird_backends ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
