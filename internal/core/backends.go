package core

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// backendQueueSize is how many requests can wait to be sent to a backend. A
// request that finds the queue full waits for room until it times out; nothing
// is dropped. Requests go out as fast as the backend reads them, so the queue
// only absorbs bursts.
const backendQueueSize = 10

// backend is a connected chat backend: the requests waiting to be sent to it
// and the callers waiting for its replies.
type backend struct {
	id BackendID

	// requests are sent to the backend by its IngestEvents handler.
	requests chan *pb.ChatRequest

	// done is closed when the backend disconnects.
	done      chan struct{}
	closeOnce sync.Once

	// mu guards pending and channels. issueRequest removes its pending entry
	// however it ends, so a backend that never replies doesn't leak them.
	mu       sync.Mutex
	pending  map[string]chan *pb.ChatEvent // by request ID
	channels map[string]channelInfo        // by the backend's channel ID
}

// channelInfo is what a backend has told core about one of its channels.
type channelInfo struct {
	ID          string
	DisplayName string
	Topic       string
}

// joinChannel records a channel the backend is in, replacing what was known.
func (b *backend) joinChannel(channel channelInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.channels[channel.ID] = channel
}

// leaveChannel forgets a channel.
func (b *backend) leaveChannel(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.channels, id)
}

// changeChannel updates the name and topic of a channel the backend is in. A
// channel that hasn't been joined is ignored.
func (b *backend) changeChannel(channel channelInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.channels[channel.ID]; ok {
		b.channels[channel.ID] = channel
	}
}

// channel returns what is known about a channel.
func (b *backend) channel(id string) (channelInfo, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	channel, ok := b.channels[id]
	return channel, ok
}

// channelList returns every channel the backend is in, ordered by ID.
func (b *backend) channelList() []channelInfo {
	b.mu.Lock()
	defer b.mu.Unlock()

	channels := make([]channelInfo, 0, len(b.channels))
	for _, channel := range b.channels {
		channels = append(channels, channel)
	}
	slices.SortFunc(channels, func(a, b channelInfo) int { return strings.Compare(a.ID, b.ID) })
	return channels
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
	mu       sync.Mutex
	backends map[BackendID]*backend
}

// register adds a backend. It fails if one with the same ID is connected.
func (r *backendRegistry) register(id BackendID) (*backend, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.backends[id]; exists {
		return nil, status.Errorf(codes.AlreadyExists, "backend %s is already connected", id)
	}
	if r.backends == nil {
		r.backends = make(map[BackendID]*backend)
	}

	b := &backend{
		id:       id,
		requests: make(chan *pb.ChatRequest, backendQueueSize),
		done:     make(chan struct{}),
		pending:  make(map[string]chan *pb.ChatEvent),
		channels: make(map[string]channelInfo),
	}
	r.backends[id] = b
	return b, nil
}

// get returns the connected backend with the given ID, or nil.
func (r *backendRegistry) get(id BackendID) *backend {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.backends[id]
}

// list returns the IDs of the connected backends, ordered by type and then ID.
func (r *backendRegistry) list() []BackendID {
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]BackendID, 0, len(r.backends))
	for id := range r.backends {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b BackendID) int {
		return cmp.Or(strings.Compare(a.Type, b.Type), strings.Compare(a.ID, b.ID))
	})
	return ids
}

// remove disconnects b, failing any callers still waiting on it. It is safe to
// call more than once.
func (r *backendRegistry) remove(b *backend) {
	r.mu.Lock()
	// A backend that reconnected under the same ID must not be removed.
	if r.backends[b.id] == b {
		delete(r.backends, b.id)
	}
	r.mu.Unlock()

	b.closeOnce.Do(func() { close(b.done) })
}

// issueRequest sends request to a backend and waits for its reply. The request
// is copied and given an ID; the caller's message is not modified.
//
// Errors are gRPC statuses for the client: NotFound if the backend isn't
// connected, Unavailable if it disconnects while waiting, and DeadlineExceeded
// if there is no reply within RequestTimeout.
func (s *Server) issueRequest(
	ctx context.Context, id BackendID, request *pb.ChatRequest,
) (*pb.ChatEvent, error) {
	b := s.backends.get(id)
	if b == nil {
		return nil, errBackendNotFound
	}

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
	errBackendNotFound     = status.Error(codes.NotFound, "backend not found")
	errBackendDisconnected = status.Error(codes.Unavailable, "backend disconnected")
)

// requestContextError converts the end of a request's context to a status.
func requestContextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "request timed out")
	}
	return status.FromContextError(ctx.Err()).Err()
}
