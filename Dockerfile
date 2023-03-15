FROM rust:1.68-bullseye as builder
WORKDIR /usr/src/app

# Workaround to allow arm64 builds to work properly
ENV CARGO_NET_GIT_FETCH_WITH_CLI=true

# We currently use protoc rather than relying on the protobuf-sys package
# because it greatly cuts down on build times. This may change in the future.
RUN apt-get update && apt-get install -y protobuf-compiler && rm -rf /var/lib/apt/lists/*

# Copy over only the files which specify dependencies
COPY ./Cargo.toml ./Cargo.lock ./

# We need to create a dummy main in order to get this to properly build.
RUN mkdir src && echo 'fn main() {}' > src/main.rs && cargo build --release

# Copy over the files to actually build the application.
COPY . .

# We need to make sure the update time on main.rs is newer than the temporary
# file or there are weird cargo caching issues we run into.
RUN touch src/main.rs && cargo build --release && cp -v target/release/seabird-core /usr/local/bin

# Create a new base and copy in only what we need.
FROM debian:bullseye-slim
ENV RUST_LOG=info
RUN apt-get update && apt-get install -y libssl1.1 ca-certificates && rm -rf /var/lib/apt/lists/*
COPY entrypoint.sh /usr/local/bin/seabird-entrypoint.sh
COPY --from=builder /usr/local/bin/seabird-* /usr/local/bin/
EXPOSE 11235
CMD ["/usr/local/bin/seabird-entrypoint.sh"]
