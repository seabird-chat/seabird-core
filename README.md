# seabird-core

Seabird has been an IRC bot for the last 10 years in many different
incarnations. This version is a gRPC service which exports a number of functions
to easily interact with chat services.

This server acts only as an event broker - you will need both a running chat
backend and some sort of plugin in order for anything visible to happen.

The server is implemented in Rust and uses `tonic` for a gRPC server
implementation.

## Building

The easiest way to build and deploy `seabird-core` is to use the [official
docker images](https://hub.docker.com/r/seabirdchat/seabird-core).

In order to build these, you can use one of the following:

```sh
docker build -t seabird-core:rust .
```

If you want to develop locally, simply use the normal cargo workflow in order to
build/run seabird-core:

```sh
cargo run
```

## Configuring

### Environment Variables

For production, it is generally recommended that environment variables be
configured in the environment, but for dev, both implementations will
conveniently load any `.env` file in the working directory of the running
service.

- `SEABIRD_BIND_HOST` (optional, defaults to `0.0.0.0:11235`) - which host/port to bind
  the gRPC service to. Note that it will not be tls encrypted, so you may want
  to put it behind a reverse proxy.
- `SEABIRD_TOKEN_FILE` - the file to load tokens from.
- `RUST_LOG` (optional, defaults to `info,seabird::server=trace`) - this is a
  common rust environment variable documented here because we set a default. All
  seabird functionality is exposed under `seabird`.

### Token File

The tokens file contains a mapping of `tag` to `auth_token`. Each tag will be
associated with a given auth token. It is meant as a convenience to make it
easier to identify where incoming requests are coming from.

Sending a `SIGHUP` to the server process can be used to reload the configuration
file.

As an example, the following tokens file defines the `belak` tag with an
auth_token of `hunter2`.

```json
{
  "tokens": {
    "belak": "hunter2"
  }
}
```
