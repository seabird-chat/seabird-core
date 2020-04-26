package seabird

import (
	"github.com/google/uuid"

	"github.com/belak/seabird-core/pb"
)

type Stream struct {
	ID string

	broadcast chan *pb.SeabirdEvent
}

func NewStream() *Stream {
	return &Stream{
		ID: uuid.New().String(),

		broadcast: make(chan *pb.SeabirdEvent),
	}
}

func (s *Stream) Recv() <-chan *pb.SeabirdEvent {
	return s.broadcast
}
