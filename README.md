# seabird-core

[![Static Badge](https://img.shields.io/badge/repository-blue?logo=git&label=%20&labelColor=grey&color=blue)](https://github.com/seabird-chat/seabird-core)

Seabird has been an IRC bot for the last 10 years in many different
incarnations. This version is a gRPC service which exports a number of functions
to easily interact with chat services.

This server acts only as an event broker - you will need both a running chat
backend and some sort of plugin in order for anything visible to happen.

There are currently two implementations in this repo. The Rust one, built on
`tonic`, is what runs in production today. The Go one, built on `grpc-go`, is a
port which will replace it once it has been through staging. They speak the same
protocol and share the same database, so either can be run against an existing
deployment.

## Requirements

- Rust 1.83
- Go 1.26

## Building

The Rust build uses Nix to fetch the protobuf definitions from
[seabird-chat/proto], so building and developing locally both go through the
flake:

```sh
nix build
nix develop
cargo run
```

The Go build gets its generated protobuf code from [seabird-go] instead, so it
needs nothing beyond a Go toolchain:

```sh
nix build .#seabird-core-go
go run ./cmd/seabird-core --database-url sqlite://seabird.db
go test ./...
```

The Go server also serves the gRPC reflection API, so `grpcurl` can be pointed
at it without a copy of the protos. Reflection goes through the same
authentication as everything else:

```sh
grpcurl -plaintext -H "authorization: Bearer $TOKEN" localhost:11235 list
```

## Configuring

### Environment Variables

For production, it is generally recommended that environment variables be
configured in the environment. For dev, the Rust implementation loads any `.env`
file in the working directory, and the Go implementation expects direnv or
equivalent to have put the variables in the environment already.

Shared by both implementations:

- `DATABASE_URL` (required) - where to place the sqlite database seabird-core will use.
  This should be in a URL format, so `sqlite:tokens.db` will be relative to the current
  directory and `sqlite:///path/to/tokens.db` will be absolute.

The Go implementation takes every setting as either a flag or an environment
variable, where `--bind-host` is `BIND_HOST` and so on. Run it with `--help` for
the authoritative list.

- `BIND_HOST` (optional, defaults to `0.0.0.0:11235`) - which host/port to bind
  the gRPC service to. Note that it will not be tls encrypted, so you may want
  to put it behind a reverse proxy.
- `LOG_LEVEL` (optional, defaults to `info`) - one of `debug`, `info`, `warn` or
  `error`.
- `LOG_FORMAT` (optional) - one of `json`, `text` or `pretty`. Defaults to
  `pretty` when stdout is a terminal and `json` otherwise.

The Rust implementation instead uses:

- `SEABIRD_BIND_HOST` (optional, defaults to `0.0.0.0:11235`) - the equivalent of
  `BIND_HOST` above.
- `RUST_LOG` (optional, defaults to `info,seabird::server=trace`) - this is a
  common rust environment variable documented here because we set a default. All
  seabird functionality is exposed under `seabird`.

[seabird-chat/proto]: https://github.com/seabird-chat/proto
[seabird-go]: https://github.com/seabird-chat/seabird-go
