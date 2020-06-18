# seabird-core-rs

## Building

Simply use the normal cargo workflow in order to build/run seabird-core:

```
cargo run
```

## Additional Config

- `RUST_LOG` (optional, defaults to `info,seabird::server=trace`) - this is a
  common rust environment variable documented here because we set a default. All
  seabird functionality is exposed under `seabird`.
