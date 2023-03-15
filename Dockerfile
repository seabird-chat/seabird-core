FROM rust:1.68 as builder
WORKDIR /usr/src/seabird-core

# NOTE: tonic_build uses rustfmt to properly format the output files and give
# better errors.
RUN rustup component add rustfmt

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
FROM debian:buster-slim
ENV RUST_LOG=info
RUN apt-get update && apt-get install -y libssl1.1 ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /usr/local/bin/seabird-core /usr/local/bin/seabird-core
EXPOSE 11235
CMD ["seabird-core"]
