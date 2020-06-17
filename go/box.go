package seabird

import (
	"context"
	"sync"
)

type MessageBox struct {
	bufSize       int
	mu            sync.Mutex
	subscriptions map[string]*MessageBoxHandle
}

// NewMessageBox creates a message box ready for use with the given internal
// channel size. Note that this buffer is used for *all* subscriptions, it is
// not the total size.
func NewMessageBox(bufSize int) *MessageBox {
	return &MessageBox{
		bufSize:       bufSize,
		subscriptions: make(map[string]*MessageBoxHandle),
	}
}

// Subscribe returns a new subscription handle for the given key and an ok value
// if that key was available.
func (b *MessageBox) Subscribe(key string) (*MessageBoxHandle, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.subscriptions[key] != nil {
		return nil, false
	}

	handle := &MessageBoxHandle{
		box: b,
		key: key,
		c:   make(chan interface{}, b.bufSize),
	}

	b.subscriptions[key] = handle

	return handle, true
}

func (b *MessageBox) sendLocked(handle *MessageBoxHandle, data interface{}) {
	// NOTE: this can only be called from inside a locked mutex.

	// If we failed to send, delete the current key
	if !handle.Send(data) {
		handle.close()
		delete(b.subscriptions, handle.key)
	}
}

// Broadcast will send a message to all subscriptions.
func (b *MessageBox) Broadcast(data interface{}) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, handle := range b.subscriptions {
		b.sendLocked(handle, data)
	}
}

// Send will send a message to a single subscription.
func (b *MessageBox) Send(key string, data interface{}) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	handle, ok := b.subscriptions[key]
	if !ok {
		return false
	}

	b.sendLocked(handle, data)

	return true
}

func (b *MessageBox) removeSubscription(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.subscriptions, key)
}

type MessageBoxHandle struct {
	key string
	mu  sync.RWMutex
	box *MessageBox
	c   chan interface{}
}

func (h *MessageBoxHandle) Send(data interface{}) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// TODO: if send fails, call .Close - this could cause deadlocks, so be
	// careful.

	// Try to send, but if it fails, ignore it.
	select {
	case h.c <- data:
		return true
	default:
		return false
	}
}

// Recv will receive a value from this subscription, along with an ok value for
// if we failed to get an item.
func (h *MessageBoxHandle) Recv(ctx context.Context) (interface{}, bool) {
	// We need to pull the chan value because it gets set to nil when closed.
	h.mu.RLock()
	incoming := h.c
	h.mu.RUnlock()

	// Reading from a nil chan will cause a hang, so if we have a nil chan, just
	// return a failure.
	if h.c == nil {
		return nil, false
	}

	select {
	case data, ok := <-incoming:
		return data, ok
	case <-ctx.Done():
		return nil, false
	}
}

func (h *MessageBoxHandle) close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	// We don't want multiple calls to close to cause a panic
	if h.c != nil {
		close(h.c)
		h.c = nil
	}
}

// Close shuts down all receivers and removes this subscription from the box.
func (h *MessageBoxHandle) Close() {
	h.close()
	h.box.removeSubscription(h.key)
}
