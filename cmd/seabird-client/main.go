//go:generate protoc -I ../../pb --go_out=plugins=grpc:../../pb/ ../../pb/seabird.proto

package main

import (
	"context"
	"io"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/belak/seabird-core/pb"
)

const (
	address = "localhost:11235"
)

func main() {
	// Set up a connection to the server.
	conn, err := grpc.Dial(address, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := pb.NewSeabirdClient(conn)

	// Contact the server and print out its response.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := c.Register(ctx, &pb.RegisterRequest{
		Identifier: "seabird-client",
	})
	if err != nil {
		log.Fatalf("could not connect: %v", err)
	}
	log.Printf("Greeting: %s", r.GetClientId())

	ctx = metadata.AppendToOutgoingContext(ctx, "client_id", r.GetClientId(), "plugin", "seabird-client")

	/*
		resp, err := c.SendMessage(ctx, &pb.SendMessageRequest{Target: "#encoded-test", Message: "hello world"})
		if err != nil {
			log.Fatalf("could not send message: %v", err)
		}

		log.Printf("Resp: %v\n", resp)
	*/

	time.Sleep(5 * time.Second)

	stream, err := c.EventStream(ctx, &pb.EventStreamRequest{})
	if err != nil {
		log.Fatalf("could not get event stream: %v", err)
	}

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}

		if err != nil {
			log.Fatalf("could not get event: %v", err)
		}

		log.Printf("Msg: %v", msg)

		resp, err := c.SendReplyMessage(ctx, &pb.SendReplyMessageRequest{Target: msg, Message: "no u"})
		if err != nil {
			log.Fatalf("could not respond: %v", err)
		}

		log.Printf("Resp: %v", resp)
	}
}
