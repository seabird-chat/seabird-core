//go:generate protoc -I ../../pb --go_out=plugins=grpc:../../pb/ ../../pb/seabird.proto

package main

import (
	"context"
	"encoding/base64"
	"io"
	"log"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	"github.com/belak/seabird-core/pb"
)

type basicAuth struct {
	username string
	password string
}

func (b basicAuth) GetRequestMetadata(ctx context.Context, in ...string) (map[string]string, error) {
	auth := b.username + ":" + b.password
	enc := base64.StdEncoding.EncodeToString([]byte(auth))
	return map[string]string{
		"authorization": "Basic " + enc,
	}, nil
}

func (basicAuth) RequireTransportSecurity() bool {
	return true
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Set up a connection to the server.
	conn, err := grpc.DialContext(ctx, os.Args[1],
		grpc.WithTransportCredentials(credentials.NewTLS(nil)),
		grpc.WithPerRPCCredentials(basicAuth{
			username: os.Getenv("GRPC_USER"),
			password: os.Getenv("GRPC_PASS"),
		}), grpc.WithBlock())
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	c := pb.NewSeabirdClient(conn)

	// Contact the server and print out its response.
	r, err := c.Register(ctx, &pb.RegisterRequest{
		Identifier: "seabird-client",
	})
	if err != nil {
		log.Fatalf("could not connect: %v", err)
	}
	log.Printf("Greeting: %s", r.GetClientId())

	ctx = metadata.AppendToOutgoingContext(ctx, "client_id", r.GetClientId(), "plugin", "seabird-client")

	resp, err := c.GetChannel(ctx, &pb.ChannelRequest{Name: "#encoded-test"})
	if err != nil {
		log.Fatalf("could not get channel metadata: %v", err)
	}
	log.Printf("Chan resp: %v\n", resp)

	_, err = c.SendMessage(ctx, &pb.SendMessageRequest{Target: "#encoded-test", Message: resp.Topic})
	if err != nil {
		log.Fatalf("could not send message: %v", err)
	}

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
