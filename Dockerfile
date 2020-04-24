# Stage 1: Build the application
FROM golang:1.14-buster as builder

RUN mkdir /build
RUN mkdir /seabird
WORKDIR /seabird

ADD go.mod go.sum ./
RUN go mod download

ADD . .
RUN go build -v -o /build/seabird ./cmd/seabird

# Stage 2: Copy files and configure what we need
FROM debian:buster

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Copy the built seabird into the container
COPY --from=builder /build /bin

EXPOSE 11235

ENTRYPOINT ["/bin/seabird"]
