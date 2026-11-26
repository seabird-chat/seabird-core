# seabird-core

[![Static Badge](https://img.shields.io/badge/repository-blue?logo=git&label=%20&labelColor=grey&color=blue)](https://github.com/seabird-chat/seabird-core)

Seabird has been an IRC bot for the last 10 years in many different
incarnations. This version is a gRPC service which exports a number of functions
to easily interact with chat services.

This server acts only as an event broker - you will need both a running chat
backend and some sort of plugin in order for anything visible to happen.

## Running

```sh
nix develop
just run serve --bind-host 127.0.0.1:11235 --database-url file:seabird-core.db
```

`just test` runs the tests, `just check` adds vet and the race detector, and
`just fmt` formats everything.

## Configuring

Every flag can also be set with a `SEABIRD_` environment variable:

- `SEABIRD_DATABASE_URL` (required) - the SQLite database holding auth tokens,
  as a filename or a `file:` URI. Tokens are rows in its `seabird_auth_tokens`
  table, managed with `seabird-core token` (see below). Core also records every
  backend, channel and user it sees in `seabird_backends`, `seabird_channels`
  and `seabird_users`, giving each a stable seabird ID such as
  `sbc_5k2q8mzx7v1rc`.
- `SEABIRD_BIND_HOST` (optional, defaults to `:11235`) - which host/port to bind
  the gRPC service to. It isn't TLS encrypted, so you may want to put it behind a
  reverse proxy.
- `SEABIRD_LOG_LEVEL` and `SEABIRD_LOG_FORMAT` (optional, default `info` and
  `json`) - the log level (`debug`, `info`, `warn` or `error`) and format
  (`json`, `pretty` or `text`).

## Auth tokens

Every plugin and backend authenticates with a token. The `token` subcommands
take the same `--database-url` as `serve`:

```sh
seabird-core token create --database-url seabird-core.db my-plugin
seabird-core token list --database-url seabird-core.db
seabird-core token delete --database-url seabird-core.db 3
```

`create` prints the new token, which starts with `sbt_`; `list` never shows
keys. A name can have several tokens, so a token can be replaced without
downtime by creating the new one before deleting the old.

## Seabird IDs

`backend list`, `channel list` and `user list` show each seabird ID next to the
address plugins currently use for it, like `irc://libera/%23chan`. `backend
seed`, `channel seed` and `user seed` create one ahead of time for an address
core hasn't seen yet, and print its ID. Anything that already has an ID keeps
it:

```sh
seabird-core channel seed --database-url seabird-core.db 'irc://libera/%23chan'
seabird-core channel list --database-url seabird-core.db
```

Seeded rows show a last seen of `never` until a backend reports them.
