# seabird-core

Seabird has been an IRC bot for the last 10 years in many different
incarnations. This version is a gRPC service which exports a number of functions
to easily interact with chat services.

## Server Implementations

There are currently two server implementations. They export the same interface
and should respond almost exactly the same. They are both designed to use the
same environment variables for configuration and the same config file format.
Either one can be used interchangeably.

This server acts only as an event broker - you will need both a running chat
backend and some sort of plugin in order for anything visible to happen.

### Go

The Go version uses the official gRPC libraries for a server implementation but
a semi-custom http Handler in order to support grpc-web.

### Rust

The Rust version uses `tonic` for a gRPC server implementation.

## Building

The easiest way to build and deploy `seabird-core` is to use the [official
docker images](https://hub.docker.com/r/seabirdchat/seabird-core). The `go` and
`rust` tags point to the Go and Rust implementations respectively on the master
branch. Additionally, the `latest` tag will point to whichever implementation is
given focus when implementing. This is currently the `rust` version.

In order to build these, you can use one of the following:

```sh
docker build -t seabird-core:go -f Dockerfile-go .
docker build -t seabird-core:rust -f Dockerfile-rust .
```

If you want to run the server outside of docker, there is information in each
implementation's sub-folder README.

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
