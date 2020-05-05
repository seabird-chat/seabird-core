package seabird

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/seabird-irc/seabird-core/pb"
)

const EVENT_STREAM_BUFFER = 10

type CommandMetadata struct {
	Name      string
	ShortHelp string
	FullHelp  string
}

type EventStream struct {
	sync.RWMutex

	id      uuid.UUID
	tag     string
	channel chan *pb.Event

	remoteAddr     net.Addr
	connectionTime time.Time

	commandMetadata map[string]*CommandMetadata
}

func NewEventStream(ctx context.Context, addr net.Addr, meta map[string]*CommandMetadata) *EventStream {
	ret := &EventStream{
		id:              uuid.New(),
		tag:             CtxTag(ctx),
		channel:         make(chan *pb.Event, EVENT_STREAM_BUFFER),
		connectionTime:  time.Now(),
		remoteAddr:      addr,
		commandMetadata: meta,
	}

	return ret
}

func (s *EventStream) RemoteAddr() net.Addr {
	return s.remoteAddr
}

func (s *EventStream) ConnectionTime() time.Time {
	return s.connectionTime
}

func (s *EventStream) Commands() []string {
	var ret []string
	for command := range s.commandMetadata {
		ret = append(ret, command)
	}
	return ret
}

func (s *EventStream) CommandInfo(command string) *CommandMetadata {
	return s.commandMetadata[command]
}

func (s *EventStream) ID() uuid.UUID {
	// NOTE: ID will never change so we don't need to lock here
	return s.id
}

func (s *EventStream) Tag() string {
	// NOTE: Tag will never change so we don't need to lock here
	return s.tag
}

func (s *EventStream) Send(e *pb.Event) error {
	s.RLock()
	defer s.RUnlock()

	select {
	case s.channel <- e:
		return nil
	default:
		return fmt.Errorf("stream %s lagging", s.id)
	}
}

func (s *EventStream) Recv(ctx context.Context) (*pb.Event, error) {
	// NOTE: this doesn't need to lock the mutex because closing the channel
	// will cause this to exit cleanly. The mutex is there because sending to a
	// closed channel will panic.
	select {
	case e, ok := <-s.channel:
		if !ok {
			return nil, fmt.Errorf("tried to send to closed event stream %s", s.id)
		}
		return e, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *EventStream) Close() {
	// TODO: right now this will be automatically cleaned up when the next event
	// is sent, but it may be worthwhile to drop this event stream when this is
	// called.
	s.Lock()
	defer s.Unlock()

	if s.channel != nil {
		close(s.channel)
		s.channel = nil
	}
}
