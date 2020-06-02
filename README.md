# seabird-core

Seabird has been an IRC bot for the last 10 years in many different
incarnations. This version is a gRPC service which exports a number of functions
to easily interact with IRC.

## Server Implementations

There are currently two server implementations. They export the same interface
and should respond almost exactly the same. They are both designed to use the
same environment variables for configuration and the same config file format.

### Go

The Go version uses the official gRPC libraries for a server implementation but
a semi-custom http Handler in order to support grpc-web without an additional
proxy.

### Rust

The Rust version uses `tonic` for a gRPC server implementation.

## Building

The easiest way to build and deploy `seabird-core` is to use the [official
docker images](https://hub.docker.com/r/belak/seabird-core). The `go` and `rust`
tags point to their specific implementations.

In order to build these, you can use the following:

```sh
docker build -f Dockerfile-go -t seabird-core:go .
docker build -f Dockerfile-rust -t seabird-core:rust .
```

Specific information about building/running each implementation locally is
included in that implementation's README.

## Configuring

### Environment Variables

For production, it is generally recommended that environment variables be
configured in the environment, but for dev, both implementations will
conveniently load any `.env` file in the working directory of the running
service.

- `SEABIRD_IRC_HOST` - which irc server to connect to. This accepts the irc,
  ircs, and ircs+unsafe schemes, depending on the connection.
- `SEABIRD_BIND_HOST` (optional, defaults to `:11235`) - which host/port to bind
  the gRPC service to. Note that it will not be tls encrypted, so you may want
  to put it behind a reverse proxy.
- `SEABIRD_NICK` - nick to use when connecting to IRC
- `SEABIRD_USER` (optional, defaults to `SEABIRD_NICK`) - username to use when connecting to IRC
- `SEABIRD_NAME` (optional, defaults to `SEABIRD_USER`) - name to use when connecting to IRC
- `SEABIRD_PASS` (optional) - password to use when connecting to IRC
- `SEABIRD_COMMAND_PREFIX` (optional, defaults to `!`)
- `SEABIRD_TOKEN_FILE` - the file to load tokens from. Note that this file will
  be watched for changes so a token change will not require a bot restart.

### Token File

The tokens key is a mapping of `tag` to `auth_token`. Each tag will be
associated with a given auth token. It is meant as a convenience to make it
easier to identify where incoming requests are coming from.

As was mentioned before, this file will be watched for changes so tokens will go
into effect without a restart of the service.

As an example, the following tokens file defines the `belak` tag with an
auth_token of `hunter2`.

```json
{
  "tokens": {
    "belak": "hunter2"
  }
}
```
