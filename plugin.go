package seabird

import (
	"context"
	"sort"
	"sync"

	"github.com/belak/seabird-core/pb"
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

func (p *Plugin) Close() {
	p.Lock()
	defer p.Unlock()

	// Close any currently open streams
	for _, stream := range p.streams {
		stream.Close()
	}

	// Nuke the streams
	p.streams = make(map[string]*Stream)
}

func (p *Plugin) Commands() []string {
	p.RLock()
	defer p.RUnlock()

	var ret []string
	for key := range p.commands {
		ret = append(ret, key)
	}

	sort.Strings(ret)

	return ret
}

func (p *Plugin) CommandInfo(name string) *commandMetadata {
	p.RLock()
	defer p.RUnlock()

	return p.commands[name]
}

func (p *Plugin) RespondsToCommand(command string) bool {
	p.RLock()
	defer p.RUnlock()

	_, ok := p.commands[command]

	return ok
}

func (p *Plugin) Broadcast(ctx context.Context, event *pb.SeabirdEvent) error {
	p.RLock()
	defer p.RUnlock()

	for _, stream := range p.streams {
		err := stream.Send(ctx, event)
		if err != nil {
			return err
		}
	}

	return nil
}
