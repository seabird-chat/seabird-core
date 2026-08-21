package core

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const testToken = "test-token"

// startTestServer brings up a Server on an in-memory listener with a single
// auth token registered, and returns a connection to it.
func startTestServer(t *testing.T) *grpc.ClientConn {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server, err := New(ctx, Config{
		DatabaseURL: "sqlite://" + filepath.Join(t.TempDir(), "core.db"),
		Logger:      logger,
	})
	assert.NoError(t, err)
	t.Cleanup(func() { server.Close() })

	_, err = server.db.inner.ExecContext(ctx,
		"INSERT INTO seabird_auth_tokens (name, key) VALUES (?, ?)", "test-plugin", testToken)
	assert.NoError(t, err)

	listener := bufconn.Listen(1024 * 1024)

	served := make(chan error, 1)
	go func() { served <- server.serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		<-served
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	assert.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return conn
}

// authedContext returns a context carrying the test token, plus a deadline so a
// hung stream fails the test instead of hanging it.
func authedContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+testToken)
}

// connectBackend opens an ingest stream and completes the Hello handshake.
func connectBackend(t *testing.T, conn *grpc.ClientConn, backendType, id string) pb.ChatIngest_IngestEventsClient {
	t.Helper()

	stream, err := pb.NewChatIngestClient(conn).IngestEvents(authedContext(t))
	assert.NoError(t, err)

	err = stream.Send(&pb.ChatEvent{Inner: &pb.ChatEvent_Hello{Hello: &pb.HelloChatEvent{
		BackendInfo: &pb.Backend{Type: backendType, Id: id},
	}}})
	assert.NoError(t, err)

	return stream
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	conn := startTestServer(t)
	client := pb.NewSeabirdClient(conn)

	_, err := client.GetCoreInfo(context.Background(), &pb.CoreInfoRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer wrong")
	_, err = client.GetCoreInfo(ctx, &pb.CoreInfoRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestBackendRegistration(t *testing.T) {
	conn := startTestServer(t)
	client := pb.NewSeabirdClient(conn)

	connectBackend(t, conn, "irc", "testnet")

	// Registration happens on the server after the Hello is received, so poll
	// until it shows up rather than racing it.
	var backends []*pb.Backend

	assert.NoError(t, eventually(func() bool {
		resp, err := client.ListBackends(authedContext(t), &pb.ListBackendsRequest{})
		if err != nil {
			return false
		}

		backends = resp.GetBackends()

		return len(backends) == 1
	}))

	assert.Equal(t, "irc://testnet", backends[0].GetId())
	assert.Equal(t, "irc", backends[0].GetType())

	// A second backend claiming the same ID has to be turned away.
	dup, err := pb.NewChatIngestClient(conn).IngestEvents(authedContext(t))
	assert.NoError(t, err)
	assert.NoError(t, dup.Send(&pb.ChatEvent{Inner: &pb.ChatEvent_Hello{Hello: &pb.HelloChatEvent{
		BackendInfo: &pb.Backend{Type: "irc", Id: "testnet"},
	}}}))

	_, err = dup.Recv()
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
}

func TestChatEventIsRelayedToPlugins(t *testing.T) {
	conn := startTestServer(t)

	events, err := pb.NewSeabirdClient(conn).StreamEvents(authedContext(t), &pb.StreamEventsRequest{})
	assert.NoError(t, err)

	backend := connectBackend(t, conn, "irc", "testnet")

	// StreamEvents subscribes before returning its first message, so keep
	// resending until an event makes it through.
	send := func() {
		assert.NoError(t, backend.Send(&pb.ChatEvent{
			Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
				Source: &pb.ChannelSource{
					ChannelId: "#seabird",
					User:      &pb.User{Id: "belak", DisplayName: "belak"},
				},
				Text: "hello",
			}},
		}))
	}

	received := make(chan *pb.Event, 1)

	go func() {
		event, err := events.Recv()
		if err == nil {
			received <- event
		}
		close(received)
	}()

	var event *pb.Event

	assert.NoError(t, eventually(func() bool {
		send()

		select {
		case event = <-received:
			return event != nil
		case <-time.After(50 * time.Millisecond):
			return false
		}
	}))

	msg := event.GetMessage()
	assert.Equal(t, "hello", msg.GetText())
	assert.Equal(t, "irc://testnet/%23seabird", msg.GetSource().GetChannelId())
	assert.Equal(t, "irc://testnet/belak", msg.GetSource().GetUser().GetId())

	// The text-only API was used, so the block tree is synthesized and tagged.
	assert.Equal(t, "hello", msg.GetRootBlock().GetPlain())
	assert.Equal(t, "text", event.GetTags()[originalFormatTag])
}

func TestSendMessageRoundTrip(t *testing.T) {
	conn := startTestServer(t)
	client := pb.NewSeabirdClient(conn)

	backend := connectBackend(t, conn, "irc", "testnet")

	// The backend has to answer before the RPC's one second deadline, so the
	// responder runs concurrently with the call.
	requests := make(chan *pb.ChatRequest, 1)

	go func() {
		req, err := backend.Recv()
		if err != nil {
			close(requests)
			return
		}

		requests <- req

		_ = backend.Send(&pb.ChatEvent{
			Id:    req.GetId(),
			Inner: &pb.ChatEvent_Success{Success: &pb.SuccessChatEvent{}},
		})
	}()

	assert.NoError(t, eventually(func() bool {
		_, err := client.SendMessage(authedContext(t), &pb.SendMessageRequest{
			ChannelId: "irc://testnet/%23seabird",
			Text:      "hello",
		})

		return err == nil
	}))

	req := <-requests
	sent := req.GetSendMessage()
	assert.NotZero(t, sent)

	// Backends only ever see backend-relative IDs.
	assert.Equal(t, "#seabird", sent.GetChannelId())
	assert.Equal(t, "hello", sent.GetText())
	assert.Equal(t, "hello", sent.GetRootBlock().GetPlain())
	assert.Equal(t, "text", sent.GetTags()[originalFormatTag])
}

func TestSendMessageToUnknownBackend(t *testing.T) {
	conn := startTestServer(t)

	_, err := pb.NewSeabirdClient(conn).SendMessage(authedContext(t), &pb.SendMessageRequest{
		ChannelId: "irc://nope/%23seabird",
		Text:      "hello",
	})
	assert.Equal(t, codes.NotFound, status.Code(err))

	_, err = pb.NewSeabirdClient(conn).SendMessage(authedContext(t), &pb.SendMessageRequest{
		ChannelId: "not-an-id",
		Text:      "hello",
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCommandsAreRegisteredForTheLifeOfAStream(t *testing.T) {
	conn := startTestServer(t)
	client := pb.NewSeabirdClient(conn)

	ctx, cancel := context.WithCancel(authedContext(t))

	_, err := client.StreamEvents(ctx, &pb.StreamEventsRequest{
		Commands: map[string]*pb.CommandMetadata{
			"ping": {Name: "ping", ShortHelp: "pong"},
		},
	})
	assert.NoError(t, err)

	assert.NoError(t, eventually(func() bool {
		resp, err := client.RegisteredCommands(authedContext(t), &pb.CommandsRequest{})

		return err == nil && resp.GetCommands()["ping"] != nil
	}))

	// A second plugin can't claim a command another one already holds.
	conflict, err := client.StreamEvents(authedContext(t), &pb.StreamEventsRequest{
		Commands: map[string]*pb.CommandMetadata{"ping": {Name: "ping"}},
	})
	assert.NoError(t, err)

	_, err = conflict.Recv()
	assert.Equal(t, codes.AlreadyExists, status.Code(err))

	// Disconnecting releases the command again.
	cancel()

	assert.NoError(t, eventually(func() bool {
		resp, err := client.RegisteredCommands(authedContext(t), &pb.CommandsRequest{})

		return err == nil && len(resp.GetCommands()) == 0
	}))
}

// eventually retries cond until it passes or the budget runs out. Several
// assertions here race server-side bookkeeping which has no completion signal
// on the wire.
func eventually(cond func() bool) error {
	deadline := time.Now().Add(5 * time.Second)

	for {
		if cond() {
			return nil
		}

		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}

		time.Sleep(10 * time.Millisecond)
	}
}
