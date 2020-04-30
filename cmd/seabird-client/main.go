//go:generate protoc -I ../../proto --go_out=plugins=grpc:../../pb/ ../../proto/seabird.proto

package main

import (
	"context"
	"encoding/base64"
	"io"
	"log"
	"os"
	"time"

	"github.com/belak/seabird-core/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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

	//conn, err := grpc.DialContext(ctx, "localhost:11235", grpc.WithInsecure(), grpc.WithBlock()) /*
	// Set up a connection to the server.
	conn, err := grpc.DialContext(ctx, os.Args[1],
		grpc.WithTransportCredentials(credentials.NewTLS(nil)),
		grpc.WithPerRPCCredentials(basicAuth{
			username: os.Getenv("GRPC_USER"),
			password: os.Getenv("GRPC_PASS"),
		}), grpc.WithBlock())
	//*/
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	c := pb.NewSeabirdClient(conn)

	// Contact the server and print out its response.
	r, err := c.OpenSession(ctx, &pb.OpenSessionRequest{
		Plugin: "seabird-client",
		Commands: map[string]*pb.CommandMetadata{
			"test": {
				Name:      "test",
				ShortHelp: "just a test command",
			},
		},
	})
	if err != nil {
		log.Fatalf("could not connect: %v", err)
	}
	identity := r.GetIdentity()
	log.Printf("Greeting: %s", identity.GetToken())

	_, err = c.SendMessage(ctx, &pb.SendMessageRequest{Identity: identity, Target: "#encoded-test", Message: "HELLO WORLD"})
	if err != nil {
		log.Fatalf("could not send message: %v", err)
	}

	resp, err := c.GetChannelInfo(ctx, &pb.ChannelInfoRequest{Identity: identity, Name: "#encoded-test"})
	if err != nil {
		log.Fatalf("could not get channel metadata: %v", err)
	}
	log.Printf("Chan resp: %v\n", resp)

	_, err = c.SendMessage(ctx, &pb.SendMessageRequest{Identity: identity, Target: "#encoded-test", Message: resp.Topic})
	if err != nil {
		log.Fatalf("could not send message: %v", err)
	}

	stream, err := c.Events(ctx, &pb.EventsRequest{Identity: identity})
	if err != nil {
		log.Fatalf("could not get event stream: %v", err)
	}

	_, err = c.Events(ctx, &pb.EventsRequest{Identity: identity})
	if err != nil {
		log.Fatalf("could not get second event stream: %v", err)
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

		var replyTo string
		switch event := msg.Inner.(type) {
		case *pb.Event_Message:
			replyTo = event.Message.ReplyTo
		case *pb.Event_PrivateMessage:
			replyTo = event.PrivateMessage.ReplyTo
		case *pb.Event_Command:
			replyTo = event.Command.ReplyTo
		default:
			continue
		}

		resp, err := c.SendMessage(ctx, &pb.SendMessageRequest{Identity: identity, Target: replyTo, Message: "no u"})
		if err != nil {
			log.Fatalf("could not respond: %v", err)
		}

		log.Printf("Resp: %v", resp)
	}
}
