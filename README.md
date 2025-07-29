# seabird-core

Junk change

[![Static Badge](https://img.shields.io/badge/repository-blue?logo=git&label=%20&labelColor=grey&color=blue)](https://github.com/seabird-chat/seabird-core)
[![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/seabird-chat/seabird-core/docker-publish.yml)](https://github.com/seabird-chat/seabird-core/actions/workflows/docker-publish.yml)

Seabird has been an IRC bot for the last 10 years in many different
incarnations. This version is a gRPC service which exports a number of functions
to easily interact with chat services.

This server acts only as an event broker - you will need both a running chat
backend and some sort of plugin in order for anything visible to happen.

The server is implemented in Rust and uses `tonic` for a gRPC server
implementation.

## Requirements

- Rust 1.70

## Building

The easiest way to build and deploy `seabird-core` is to use the [official
docker image](https://hub.docker.com/r/seabirdchat/seabird-core).

In order to build this, you can use the following:

```sh
docker build -t seabird-core:latest .
```

If you want to develop locally, simply use the normal cargo workflow in order to
build/run seabird-core:

```sh
cargo run
```

Note that because this generates code based on the protobufs, you may need to run
`git submodule update --init` to make sure they are up to date.

## Configuring

### Environment Variables

For production, it is generally recommended that environment variables be
configured in the environment, but for dev, both implementations will
conveniently load any `.env` file in the working directory of the running
service.

- `DATABASE_URL` (required) - where to place the sqlite database seabird-core will use.
  This should be in a URL format, so `sqlite:tokens.db` will be relative to the current
  directory and `sqlite:///path/to/tokens.db` will be absolute.
- `SEABIRD_BIND_HOST` (optional, defaults to `0.0.0.0:11235`) - which host/port to bind
  the gRPC service to. Note that it will not be tls encrypted, so you may want
  to put it behind a reverse proxy.
- `RUST_LOG` (optional, defaults to `info,seabird::server=trace`) - this is a
  common rust environment variable documented here because we set a default. All
  seabird functionality is exposed under `seabird`.
