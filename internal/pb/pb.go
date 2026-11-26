// Package pb is the code generated from the protos in the proto submodule.
// Run `just generate` after changing them.
package pb

//go:generate protoc --proto_path=../../proto --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative:. common.proto seabird.proto seabird_chat_ingest.proto
