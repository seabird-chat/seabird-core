package core

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"

	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// backendQueueSize is how many requests can wait to be sent to a backend. A
// request that finds the queue full waits for room until it times out; nothing
// is dropped. Requests go out as fast as the backend reads them, so the queue
// only absorbs bursts.
const backendQueueSize = 10

// backendKey is the type and name a backend gives in its hello. Only one
// backend can be connected with each.
type backendKey struct {
	Type string
	Name string
}

// backend is a connected chat backend: the requests waiting to be sent to it
// and the callers waiting for its replies.
type backend struct {
	key backendKey

	// features are what the backend said it supports in its hello.
	features map[pb.BackendFeature]bool

	// row is the backend as recorded in the database, whose ID plugins use. It
	// is set before the backend can be found by that ID.
	row *Backend

	// requests are sent to the backend by its IngestEvents handler.
	requests chan *pb.ChatRequest

	// relays are chat from other members of its channels' groups, sent to it
	// by runRelays.
	relays chan relay

	// done is closed when the backend stops taking requests.
	done      chan struct{}
	closeOnce sync.Once

	// mu guards pending. issueRequest removes its entry however it ends, so a
	// backend that never replies doesn't leak them.
	mu      sync.Mutex
	pending map[string]chan *pb.ChatEvent // by request ID
}

// expectReply registers a caller waiting for the reply to request id.
func (b *backend) expectReply(id string) <-chan *pb.ChatEvent {
	// Buffered so respond never waits for the caller.
	reply := make(chan *pb.ChatEvent, 1)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.pending[id] = reply
	return reply
}

// forget stops waiting for the reply to request id.
func (b *backend) forget(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.pending, id)
}

// respond delivers event to the caller waiting for request id and reports
// whether there was one. Each request is answered at most once.
func (b *backend) respond(id string, event *pb.ChatEvent) bool {
	b.mu.Lock()
	reply, ok := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()

	if ok {
		reply <- event
	}
	return ok
}

// backendRegistry tracks the connected backends. Its zero value is ready to use.
type backendRegistry struct {
	mu    sync.Mutex
	byKey map[backendKey]*backend
	byID  map[string]*backend // by row ID, once activated
}

// register reserves key for a new backend with the given features. It fails if
// a backend with the same key is connected, or hasn't finished disconnecting.
func (r *backendRegistry) register(key backendKey, features []pb.BackendFeature) (*backend, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byKey[key]; exists {
		return nil, status.Errorf(codes.AlreadyExists, "backend %s %q is already connected", key.Type, key.Name)
	}
	if r.byKey == nil {
		r.byKey = make(map[backendKey]*backend)
		r.byID = make(map[string]*backend)
	}

	b := &backend{
		key:      key,
		features: make(map[pb.BackendFeature]bool, len(features)),
		requests: make(chan *pb.ChatRequest, backendQueueSize),
		relays:   make(chan relay, relayQueueSize),
		done:     make(chan struct{}),
		pending:  make(map[string]chan *pb.ChatEvent),
	}
	for _, feature := range features {
		b.features[feature] = true
	}
	r.byKey[key] = b
	return b, nil
}

// activate makes b reachable by its row's ID.
func (r *backendRegistry) activate(b *backend, row *Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()

	b.row = row
	r.byID[row.ID] = b
}

// get returns the connected backend with the given row ID, or nil.
func (r *backendRegistry) get(id string) *backend {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.byID[id]
}

// deactivate stops b taking requests, failing any callers still waiting on
// it, but keeps its key reserved. It is safe to call more than once.
func (r *backendRegistry) deactivate(b *backend) {
	r.mu.Lock()
	if b.row != nil && r.byID[b.row.ID] == b {
		delete(r.byID, b.row.ID)
	}
	r.mu.Unlock()

	b.closeOnce.Do(func() { close(b.done) })
}

// remove deactivates b and releases its key. It is safe to call more than
// once.
func (r *backendRegistry) remove(b *backend) {
	r.deactivate(b)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.byKey[b.key] == b {
		delete(r.byKey, b.key)
	}
}

// issueRequest sends request to a backend and waits for its reply. The request
// is copied and given an ID; the caller's message is not modified.
//
// Errors are gRPC statuses for the client: Unavailable if the backend
// disconnects while waiting, and DeadlineExceeded if there is no reply within
// RequestTimeout.
func (s *Server) issueRequest(
	ctx context.Context, b *backend, request *pb.ChatRequest,
) (*pb.ChatEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, s.requestTimeout())
	defer cancel()

	requestID := rand.Text()
	reply := b.expectReply(requestID)
	defer b.forget(requestID)

	outgoing := &pb.ChatRequest{Id: requestID, Inner: request.GetInner()}

	select {
	case b.requests <- outgoing:
	case <-b.done:
		return nil, errBackendDisconnected
	case <-ctx.Done():
		return nil, requestContextError(ctx)
	}

	select {
	case event := <-reply:
		return event, nil
	case <-b.done:
		return nil, errBackendDisconnected
	case <-ctx.Done():
		return nil, requestContextError(ctx)
	}
}

var (
	errBackendNotConnected = status.Error(codes.Unavailable, "backend not connected")
	errBackendDisconnected = status.Error(codes.Unavailable, "backend disconnected")
)

// requestContextError converts the end of a request's context to a status.
func requestContextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "request timed out")
	}
	return status.FromContextError(ctx.Err()).Err()
}
