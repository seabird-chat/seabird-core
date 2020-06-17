# seabird-core-go

## Building

```sh
go generate ./...
go run ./cmd/seabird-core
```

## Additional Config

- `SEABIRD_ENABLE_WEB` (optional, defaults to true) - whether or not to enable
  grpc-web
