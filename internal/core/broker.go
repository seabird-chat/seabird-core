package core

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/seabird-chat/seabird-core/internal/pb"
)

// ErrLagged is returned by EventSubscription.Next when the subscriber fell
// behind and events were dropped. The subscription remains usable.
var ErrLagged = errors.New("event subscription lagged")

// EventBroker fans events out to subscribers. Delivery is live and best
// effort: a subscription only sees events published after it was created, and
// nothing is replayed. A slow subscriber never blocks Publish; it loses events
// and gets ErrLagged.
//
// Events from a single publisher reach each subscription in the order they were
// published. Events are shared between subscribers and must not be modified
// after publishing; clone one before changing it.
//
// Implementations must be safe for concurrent use.
type EventBroker interface {
	// Publish accepts event for the current subscribers without waiting for
	// them, and succeeds even if there are none. It returns an error only if
	// the event could not be accepted.
	Publish(ctx context.Context, event *pb.Event) error

	// Subscribe creates a subscription. ctx only bounds this call, not the
	// subscription's lifetime. The caller must Close the subscription.
	Subscribe(ctx context.Context) (EventSubscription, error)
}

// EventSubscription delivers events to a single reader. Next must not be called
// from more than one goroutine at a time; Close may be called from any.
type EventSubscription interface {
	// Next blocks until the next event. It returns ErrLagged if events were
	// dropped, ctx.Err() if ctx is done, and io.EOF once the subscription is
	// closed. After ErrLagged or ctx.Err() the subscription can still be read.
	Next(ctx context.Context) (*pb.Event, error)

	// Close ends the subscription and wakes a blocked Next. It is safe to call
	// more than once.
	Close() error
}

// memoryBrokerBufferSize is how many undelivered events a subscription holds
// before considering a stream "lagged" and dropping old events.
const memoryBrokerBufferSize = 32

var _ EventBroker = (*MemoryBroker)(nil)

// MemoryBroker is an in-process EventBroker. Its zero value is ready to use.
type MemoryBroker struct {
	mu            sync.Mutex
	subscriptions map[*memorySubscription]struct{}
}

func (b *MemoryBroker) Publish(ctx context.Context, event *pb.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event == nil {
		return errors.New("can't publish a nil event")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	for sub := range b.subscriptions {
		// If the queue is full, drop its oldest event to make room and note the
		// gap for the reader.
		if len(sub.events) == memoryBrokerBufferSize {
			sub.events = slices.Delete(sub.events, 0, 1)
			sub.lagged = true
		}
		sub.events = append(sub.events, event)

		// Next checks the queue before it waits, so one pending token is enough
		// to wake the reader, and Publish never has to block on it.
		select {
		case sub.ready <- struct{}{}:
		default:
		}
	}
	return nil
}

func (b *MemoryBroker) Subscribe(ctx context.Context) (EventSubscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.subscriptions == nil {
		b.subscriptions = make(map[*memorySubscription]struct{})
	}

	sub := &memorySubscription{
		broker: b,
		events: make([]*pb.Event, 0, memoryBrokerBufferSize),
		ready:  make(chan struct{}, 1),
	}
	b.subscriptions[sub] = struct{}{}
	return sub, nil
}

// memorySubscription is one subscriber's queue, guarded by the broker's mutex.
type memorySubscription struct {
	broker *MemoryBroker

	events []*pb.Event   // undelivered, oldest first
	ready  chan struct{} // wakes a blocked Next; may hold a stale token
	lagged bool          // an event was dropped and not yet reported
	closed bool
}

func (s *memorySubscription) Next(ctx context.Context) (*pb.Event, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		s.broker.mu.Lock()
		if s.closed {
			s.broker.mu.Unlock()
			return nil, io.EOF
		}
		// Recheck under the lock so a canceled caller never consumes an event.
		if err := ctx.Err(); err != nil {
			s.broker.mu.Unlock()
			return nil, err
		}

		if s.lagged {
			s.lagged = false
			s.broker.mu.Unlock()
			return nil, ErrLagged
		}
		if len(s.events) > 0 {
			event := s.events[0]
			s.events = slices.Delete(s.events, 0, 1)
			s.broker.mu.Unlock()
			return event, nil
		}
		s.broker.mu.Unlock()

		// Wait outside the lock, then recheck: the token may be stale.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.ready:
		}
	}
}

func (s *memorySubscription) Close() error {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()

	if !s.closed {
		s.closed = true
		s.events = nil

		// Unregister first so Publish can't send on the closed channel.
		delete(s.broker.subscriptions, s)
		close(s.ready)
	}
	return nil
}
