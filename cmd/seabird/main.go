//go:generate protoc -I ../../pb --go_out=plugins=grpc:../../pb/ ../../pb/seabird.proto

package main

import (
	"context"
	"log"
	"net"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belak/seabird-core/pb"
)

const (
	port = ":50052"
)

// TODO: move server implementation into root package dir

type server struct{}

func (s *server) Connect(ctx context.Context, in *pb.ConnectRequest) (*pb.ConnectResponse, error) {
	log.Printf("Received: %v", in.GetProtocolVersion())
	return &pb.ConnectResponse{ClientId: uuid.New().String()}, nil
}

func (s *server) EventStream(in *pb.EventStreamRequest, stream pb.Seabird_EventStreamServer) error {
	var err error

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for i := 0; i < 5; i++ {
		<-ticker.C

		err = stream.Send(&pb.SeabirdEvent{})
		if err != nil {
			return status.Error(codes.Internal, "failed to send message")
		}
	}

	return nil
}

func (s *server) SendMessage(context.Context, *pb.SendMessageRequest) (*pb.SendMessageResponse, error) {
	return nil, status.Error(codes.Unimplemented, "SendMessage unimplemented")
}

func (s *server) GetChannel(context.Context, *pb.ChannelRequest) (*pb.ChannelResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetChannel unimplemented")
}

func (s *server) JoinChannel(context.Context, *pb.JoinChannelRequest) (*pb.JoinChannelResponse, error) {
	return nil, status.Error(codes.Unimplemented, "JoinChannel unimplemented")
}

func (s *server) LeaveChannel(context.Context, *pb.LeaveChannelRequest) (*pb.LeaveChannelResponse, error) {
	return nil, status.Error(codes.Unimplemented, "LeaveChannel unimplemented")
}

func main() {
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	pb.RegisterSeabirdServer(s, &server{})
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
