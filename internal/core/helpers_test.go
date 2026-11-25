package core

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Tests drive the server through its gRPC services: a plugin client on one
// side and a fake backend on the other. They check only what crosses that
// boundary.

const (
	testToken    = "test-token"
	testUsername = "test-plugin"
)

// testChannel is a channel on the testEnv backend, as plugins write it and as
// the backend knows it.
const (
	testChannel     = "irc://libera/%23chan"
	testChannelPath = "#chan"
)

// newTestServer returns a Server with an in-memory database holding one token
// (testToken).
func newTestServer(t *testing.T) *Server {
	t.Helper()

	db := openTestDB(t)
	insertToken(t, db, testUsername, testToken)

	return &Server{
		DB:     db,
		Broker: &MemoryBroker{},
		Logger: slog.New(slog.DiscardHandler),
	}
}

// serveTestServer serves server and returns a client connection to it and a
// function that stops it, returning what Serve returned. The server is also
// stopped when the test ends.
func serveTestServer(t *testing.T, server *Server) (*grpc.ClientConn, func() error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(ctx, listener)
	}()

	stop := sync.OnceValue(func() error {
		cancel()
		select {
		case err := <-serverErr:
			return err
		case <-time.After(5 * time.Second):
			return errors.New("server did not stop after cancellation")
		}
	})
	t.Cleanup(func() { assert.NoError(t, stop()) })

	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	assert.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return conn, stop
}

// openTestDB returns a migrated in-memory database.
func openTestDB(t *testing.T) *DB {
	t.Helper()

	db, err := OpenDB(context.Background(), ":memory:", slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	return db
}

// withAuthHeader returns a context whose outgoing requests carry the given
// authorization header.
func withAuthHeader(ctx context.Context, value string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", value)
}

// pluginContext returns a context for calls made with a token.
func pluginContext(t *testing.T, token string) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(withAuthHeader(context.Background(), "Bearer "+token), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// testEnv is a running server with the backend irc://libera connected and a
// plugin reading events.
type testEnv struct {
	server  *Server
	conn    *grpc.ClientConn
	stop    func() error
	plugin  pb.SeabirdClient
	ctx     context.Context
	id      BackendID
	backend *fakeBackend
	events  *eventStream
}

// startTestEnv starts a testEnv, applying configure to the server before it
// starts serving.
func startTestEnv(t *testing.T, configure ...func(*Server)) *testEnv {
	t.Helper()

	server := newTestServer(t)
	for _, f := range configure {
		f(server)
	}
	conn, stop := serveTestServer(t, server)

	env := &testEnv{
		server: server,
		conn:   conn,
		stop:   stop,
		plugin: pb.NewSeabirdClient(conn),
		ctx:    pluginContext(t, testToken),
		id:     BackendID{Type: "irc", ID: "libera"},
	}
	env.backend = env.connectBackend(t, env.id)
	env.events, _ = env.openEvents(t, nil)
	return env
}

// connectBackend connects a backend and waits until plugins can see it.
func (e *testEnv) connectBackend(t *testing.T, id BackendID) *fakeBackend {
	t.Helper()

	f := dialBackend(t, e.conn)
	f.send(helloEvent(id))
	e.waitForBackend(t, id, true)
	return f
}

// waitForBackend waits until ListBackends does, or doesn't, include id.
func (e *testEnv) waitForBackend(t *testing.T, id BackendID, listed bool) {
	t.Helper()

	eventually(t, "backend "+id.String()+" to be listed or not", func() bool {
		resp, err := e.plugin.ListBackends(e.ctx, &pb.ListBackendsRequest{})
		assert.NoError(t, err)
		for _, backend := range resp.GetBackends() {
			if backend.GetId() == id.String() {
				return listed
			}
		}
		return !listed
	})
}

// openEvents starts an event stream for a plugin and waits until the server is
// delivering to it. It returns the stream and a function that ends it.
func (e *testEnv) openEvents(
	t *testing.T, commands map[string]*pb.CommandMetadata,
) (*eventStream, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(pluginContext(t, testToken))
	t.Cleanup(cancel)
	stream, err := e.plugin.StreamEvents(ctx, &pb.StreamEventsRequest{Commands: commands})
	assert.NoError(t, err)
	events := newEventStream(t, stream)

	// Nothing says when the server starts delivering, so the backend sends
	// sync events until one arrives. Events arrive in order, so everything
	// after it will too. Any later sync events are skipped by next.
	token := rand.Text()
	for range 100 {
		e.backend.send(syncEvent(token))
		timeout := time.After(20 * time.Millisecond)
	wait:
		for {
			select {
			case got := <-events.ch:
				assert.NoError(t, got.err, "event stream")
				if got.value.GetTags()[syncTag] == token {
					return events, cancel
				}
			case <-timeout:
				break wait
			}
		}
	}
	t.Fatal("the server never started delivering events")
	return nil, nil
}

// assertNothingPublished checks that plugins haven't been sent anything. Events
// arrive in order, so anything published earlier would arrive ahead of a
// sentinel the backend sends now.
func (e *testEnv) assertNothingPublished(t *testing.T) {
	t.Helper()

	e.backend.send(&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{Command: "sentinel"}}})
	got := e.events.next(t)
	assert.Equal(t, "sentinel", got.GetCommand().GetCommand(), "an event was published first: %v", got)
}

// sendMessage sends text to testChannel in the background, as the test plugin.
func (e *testEnv) sendMessage(ctx context.Context, text string) <-chan error {
	return callAsync(func() error {
		_, err := e.plugin.SendMessage(ctx, &pb.SendMessageRequest{ChannelId: testChannel, Text: text})
		return err
	})
}

// syncTag marks the events openEvents sends; eventStream skips them.
const syncTag = "test/sync"

// syncEvent is a command event carrying a sync token.
func syncEvent(token string) *pb.ChatEvent {
	return &pb.ChatEvent{
		Tags:  map[string]string{syncTag: token},
		Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{Command: "sync"}},
	}
}

// eventStream reads a plugin's event stream in the background, so waiting for
// an event can time out without losing it.
type eventStream struct {
	ch chan result[*pb.Event]
}

// newEventStream starts reading stream until it ends or the test does.
func newEventStream(t *testing.T, stream pb.Seabird_StreamEventsClient) *eventStream {
	s := &eventStream{ch: make(chan result[*pb.Event])}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })

	// Unbuffered, so a test that stops reading looks like a slow plugin.
	go func() {
		for {
			event, err := stream.Recv()
			select {
			case s.ch <- result[*pb.Event]{event, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

// recv returns the next event that isn't a sync event, or the error that
// ended the stream.
func (s *eventStream) recv(t *testing.T) (*pb.Event, error) {
	t.Helper()

	for {
		got := waitFor(t, s.ch, "an event")
		if got.err != nil || got.value.GetTags()[syncTag] == "" {
			return got.value, got.err
		}
	}
}

// next returns the next event that isn't a sync event, failing the test if the
// stream ends.
func (s *eventStream) next(t *testing.T) *pb.Event {
	t.Helper()

	event, err := s.recv(t)
	assert.NoError(t, err)
	return event
}

// fakeBackend is a chat backend connected to a test server.
type fakeBackend struct {
	t      *testing.T
	stream pb.ChatIngest_IngestEventsClient
	cancel context.CancelFunc
}

// dialBackend opens an ingest stream without introducing the backend.
func dialBackend(t *testing.T, conn *grpc.ClientConn) *fakeBackend {
	t.Helper()

	ctx, cancel := context.WithTimeout(withAuthHeader(context.Background(), "Bearer "+testToken), 30*time.Second)
	t.Cleanup(cancel)

	stream, err := pb.NewChatIngestClient(conn).IngestEvents(ctx)
	assert.NoError(t, err)

	return &fakeBackend{t: t, stream: stream, cancel: cancel}
}

// helloEvent is the first event a backend with the given ID sends.
func helloEvent(id BackendID) *pb.ChatEvent {
	return &pb.ChatEvent{Inner: &pb.ChatEvent_Hello{Hello: &pb.HelloChatEvent{
		BackendInfo: &pb.Backend{Type: id.Type, Id: id.ID},
	}}}
}

// send sends event to the server, failing the test if it can't.
func (f *fakeBackend) send(event *pb.ChatEvent) {
	f.t.Helper()
	assert.NoError(f.t, f.stream.Send(event))
}

// recv waits for the next request or the end of the stream.
func (f *fakeBackend) recv() (*pb.ChatRequest, error) {
	f.t.Helper()

	got := waitFor(f.t, runAsync(f.stream.Recv), "a message from the server")
	return got.value, got.err
}

// reply answers a request with a copy of event, so tests can share events.
func (f *fakeBackend) reply(request *pb.ChatRequest, event *pb.ChatEvent) {
	f.t.Helper()

	event = proto.Clone(event).(*pb.ChatEvent)
	event.Id = request.Id
	f.send(event)
}

// successEvent is a Success reply to the request with the given ID.
func successEvent(id string) *pb.ChatEvent {
	return &pb.ChatEvent{Id: id, Inner: &pb.ChatEvent_Success{Success: &pb.SuccessChatEvent{}}}
}

// chatSource is a channel and user as a backend describes them.
func chatSource(channel, userID, name string) *pb.ChannelSource {
	return &pb.ChannelSource{ChannelId: channel, User: &pb.User{Id: userID, DisplayName: name}}
}

// joinEvent, changeEvent and leaveEvent are what a backend sends when its
// channels change.
func joinEvent(channel, name, topic string) *pb.ChatEvent {
	return &pb.ChatEvent{Inner: &pb.ChatEvent_JoinChannel{JoinChannel: &pb.JoinChannelChatEvent{
		ChannelId: channel, DisplayName: name, Topic: topic,
	}}}
}

func changeEvent(channel, name, topic string) *pb.ChatEvent {
	return &pb.ChatEvent{Inner: &pb.ChatEvent_ChangeChannel{ChangeChannel: &pb.ChangeChannelChatEvent{
		ChannelId: channel, DisplayName: name, Topic: topic,
	}}}
}

func leaveEvent(channel string) *pb.ChatEvent {
	return &pb.ChatEvent{Inner: &pb.ChatEvent_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatEvent{ChannelId: channel}}}
}

// commandMetadata is the help a plugin registers for a command.
func commandMetadata(name string) *pb.CommandMetadata {
	return &pb.CommandMetadata{Name: name, ShortHelp: "usage: " + name, FullHelp: "does " + name}
}

// commandsNamed is a set of commands, as a plugin registers them.
func commandsNamed(names ...string) map[string]*pb.CommandMetadata {
	commands := make(map[string]*pb.CommandMetadata, len(names))
	for _, name := range names {
		commands[name] = commandMetadata(name)
	}
	return commands
}

// result is what a call made in the background returned.
type result[T any] struct {
	value T
	err   error
}

// runAsync makes call in the background and returns a channel for its result.
func runAsync[T any](call func() (T, error)) <-chan result[T] {
	done := make(chan result[T], 1)
	go func() {
		value, err := call()
		done <- result[T]{value, err}
	}()
	return done
}

// callAsync makes a call in the background and returns its result.
func callAsync(call func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- call() }()
	return done
}

// waitFor receives from ch, failing the test if nothing arrives in time or ch
// is closed.
func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()

	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed", what)
		}
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// eventually polls condition until it holds, failing the test if it doesn't
// within a few seconds.
func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertStatus checks that err is a gRPC status with the given code and message.
func assertStatus(t *testing.T, err error, code codes.Code, message string) {
	t.Helper()

	assert.Error(t, err)
	assert.Equal(t, code, status.Code(err))
	assert.Equal(t, message, status.Convert(err).Message())
}

// assertProtoEqual compares messages, printing both when they differ.
func assertProtoEqual(t *testing.T, want, got proto.Message) {
	t.Helper()

	if !proto.Equal(want, got) {
		t.Errorf("messages differ\nwant: %v\ngot:  %v", want, got)
	}
}

// assertProtosEqual checks that two lists of messages match, in order.
func assertProtosEqual[T proto.Message](t *testing.T, want, got []T) {
	t.Helper()

	assert.Equal(t, len(want), len(got), "number of messages")
	for i := range min(len(want), len(got)) {
		assertProtoEqual(t, want[i], got[i])
	}
}

// Reaching into internals: these helpers are the only places tests go past
// the gRPC services and the exported API, each for something the API can't do.

// insertToken adds an auth token for username to db. Operators do this with
// the sqlite CLI, so there's no API for it.
func insertToken(t *testing.T, db *DB, username, key string) {
	t.Helper()

	_, err := db.inner.ExecContext(context.Background(),
		"INSERT INTO seabird_auth_tokens (name, key) VALUES (?, ?)", username, key)
	assert.NoError(t, err)
}

// insertToken adds an auth token to the running server's database.
func (e *testEnv) insertToken(t *testing.T, username, key string) {
	t.Helper()

	insertToken(t, e.server.DB, username, key)
}

// breakDatabase closes the running server's database, so every request fails
// the way it would if the database went away.
func (e *testEnv) breakDatabase(t *testing.T) {
	t.Helper()

	assert.NoError(t, e.server.DB.Close())
}

// publishDirectly hands event to the server's broker, skipping the backend.
// It's for floods a fake backend would take too long to send.
func (e *testEnv) publishDirectly(t *testing.T, event *pb.Event) {
	t.Helper()

	assert.NoError(t, e.server.Broker.Publish(context.Background(), event))
}

// memoryBrokerCapacity is how many unread events a MemoryBroker subscription
// holds before it lags.
const memoryBrokerCapacity = memoryBrokerBufferSize
