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

The protocol is defined in the `proto` submodule, which is
[seabird-chat/proto](https://github.com/seabird-chat/proto). The code
generated from it is committed in `internal/pb`, so building doesn't need the
submodule. To change the protocol, check it out with `git submodule update
--init`, move it to the proto commit you want, and run `just generate`.

## Configuring

Every flag can also be set with a `SEABIRD_` environment variable:

- `SEABIRD_DATABASE_URL` (required) - the SQLite database holding auth tokens,
  as a filename or a `file:` URI. Tokens are rows in its `seabird_auth_tokens`
  table, managed with `seabird-core token` (see below). Backends, channels and
  users are stored in `seabird_backends`, `seabird_channels` and
  `seabird_users` (see below).
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

## Backends, channels and users

Core keeps track of every backend, channel and user it has seen, and gives each
an ID that plugins use to refer to it, such as `sbc_5k2q8mzx7v1rc`. `backend
list`, `channel list` and `user list` show them, along with what each backend
calls its channels and users:

```sh
seabird-core channel list --database-url seabird-core.db
```
