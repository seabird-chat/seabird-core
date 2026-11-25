# seabird-core

[![Static Badge](https://img.shields.io/badge/repository-blue?logo=git&label=%20&labelColor=grey&color=blue)](https://github.com/seabird-chat/seabird-core)

Seabird has been an IRC bot for the last 10 years in many different
incarnations. This version is a gRPC service which exports a number of functions
to easily interact with chat services.

This server acts only as an event broker - you will need both a running chat
backend and some sort of plugin in order for anything visible to happen.

The server is implemented in Rust and uses `tonic` for a gRPC server
implementation.

## Requirements

- Rust 1.83

## Building

`seabird-core` uses Nix to fetch the protobuf definitions from
[seabird-chat/proto], so building and developing locally both go through the
flake:

```sh
nix build
nix develop
cargo run
```

### Go port

```sh
just run serve --bind-host 127.0.0.1:11235 --database-url file:seabird-go.db
```

`--database-url` takes a SQLite filename or a `file:` URI, and can't open a
database the Rust version created. Flags can also be set from `SEABIRD_`
environment variables, like `SEABIRD_BIND_HOST`.

## Configuring

### Environment Variables

For production, it is generally recommended that environment variables be
configured in the environment, but for dev, the Rust implementation will
conveniently load any `.env` file in the working directory of the running
service.

- `DATABASE_URL` (Rust, required) - where to place the sqlite database seabird-core will use.
  This should be in a URL format, so `sqlite:tokens.db` will be relative to the current
  directory and `sqlite:///path/to/tokens.db` will be absolute.
- `SEABIRD_DATABASE_URL` (Go, required) - the same, as a SQLite filename or a `file:` URI.
- `SEABIRD_BIND_HOST` (optional, defaults to `0.0.0.0:11235`) - which host/port to bind
  the gRPC service to. Note that it will not be tls encrypted, so you may want
  to put it behind a reverse proxy.
- `SEABIRD_LOG_LEVEL` and `SEABIRD_LOG_FORMAT` (Go only, optional, default `info` and
  `json`) - the log level (`debug`, `info`, `warn` or `error`) and format (`json`,
  `pretty` or `text`).
- `RUST_LOG` (optional, defaults to `info,seabird::server=trace`) - this is a
  common rust environment variable documented here because we set a default. All
  seabird functionality is exposed under `seabird`.

[seabird-chat/proto]: https://github.com/seabird-chat/proto
