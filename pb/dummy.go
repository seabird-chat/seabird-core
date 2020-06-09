//go:generate protoc --proto_path=../proto --go_out=paths=source_relative:. --go-grpc_opt=requireUnimplementedServers=false --go-grpc_out=. ../proto/common.proto ../proto/seabird.proto ../proto/seabird_chat_ingest.proto

package pb
