_default:
    @just --list

# Build the binary into tmp/
build:
    mkdir -p tmp
    CGO_ENABLED=0 go build -o tmp/seabird-core ./cmd/seabird-core

# Remove build artifacts
clean:
    rm -rf tmp/

# Run all tests (e.g. just test -run TestIssueRequest)
test *args="":
    CGO_ENABLED=0 go test ./... {{ args }}

# Run all tests with the race detector, which needs cgo
test-race *args="":
    go test -race ./... {{ args }}

# Regenerate internal/pb from the proto submodule
generate:
    go generate ./internal/pb

# Run go vet
vet:
    go vet ./...

# Run the checks to do before committing
check: vet test-race

# Format all code (pass --fail-on-change to check only)
fmt *args="":
    treefmt {{ args }}

# Run from source (e.g. just run serve --database-url file:seabird-go.db)
run *args="":
    go run ./cmd/seabird-core {{ args }}
