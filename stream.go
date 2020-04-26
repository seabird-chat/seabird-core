package seabird

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"

	"github.com/belak/seabird-core/pb"
)

type Stream struct {
	ID string

	broadcast chan *pb.SeabirdEvent
	done      chan struct{}
	once      sync.Once
}

func NewStream() *Stream {
	return &Stream{
		ID: uuid.New().String(),

		// We buffer broadcast so there's a bit of leeway if the plugin doesn't
		// respond to an event right away.
		broadcast: make(chan *pb.SeabirdEvent, 5),

		done: make(chan struct{}),
	}
}

func (s *Stream) Send(ctx context.Context, event *pb.SeabirdEvent) error {
	select {
	case s.broadcast <- event:
	case <-s.done:
		return errors.New("Stream closed early")
	case <-ctx.Done():
		return ctx.Err()
	default:
		return errors.New("Stream queue full")
	}

	return nil
}

func (s *Stream) Recv(ctx context.Context) (*pb.SeabirdEvent, error) {
	select {
	case event := <-s.broadcast:
		return event, nil
	case <-s.done:
		return nil, errors.New("Stream closed early")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Stream) Close() {
	s.once.Do(func() {
		close(s.done)
	})
}
