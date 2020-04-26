package seabird

import (
	"sync"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Plugin struct {
	sync.RWMutex

	Name  string
	Token string

	streams  map[string]*Stream
	commands map[string]*commandMetadata

	// TODO: do something with this metric
	consecutiveDroppedMessages int
}

func NewPlugin(pluginName string, commands map[string]*commandMetadata) *Plugin {
	return &Plugin{
		Name:  pluginName,
		Token: uuid.New().String(),

		streams:  make(map[string]*Stream),
		commands: commands,
	}
}

func (p *Plugin) AddStream(stream *Stream) error {
	// Mark this stream as active
	p.Lock()
	defer p.Unlock()

	if _, ok := p.streams[stream.ID]; ok {
		return status.Error(codes.Internal, "duplicate stream id")
	}

	p.streams[stream.ID] = stream

	return nil
}

func (p *Plugin) RemoveStream(stream *Stream) {
	p.Lock()
	defer p.Unlock()

	delete(p.streams, stream.ID)
}

func (p *Plugin) Dead() bool {
	p.RLock()
	defer p.RUnlock()

	return len(p.streams) == 0
}

func (p *Plugin) RespondsToCommand(command string) bool {
	p.RLock()
	defer p.RUnlock()

	_, ok := p.commands[command]

	return ok
}
