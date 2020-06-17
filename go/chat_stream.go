package seabird

import (
	"net/url"
	"sort"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ChatChannel struct {
	ID          string
	DisplayName string
	Topic       string
}

func (cc *ChatChannel) Clone() *ChatChannel {
	if cc == nil {
		return nil
	}

	return &ChatChannel{
		ID:          cc.ID,
		DisplayName: cc.DisplayName,
		Topic:       cc.Topic,
	}
}

type ChatStream struct {
	// Reference back to the server - this should only be used when the
	// ChatStream is closed.
	serverLock sync.Mutex
	server     *Server

	eventBox           *MessageBox
	requestBoxHandle   *MessageBoxHandle
	requestResponseBox *MessageBox

	doneCallback func()

	baseID url.URL

	channelLock sync.RWMutex
	channels    map[string]*ChatChannel
}

func NewChatStream(streamType, id string, s *Server, doneCallback func()) (*ChatStream, error) {
	cs := &ChatStream{
		baseID:             url.URL{Scheme: streamType, Host: id},
		server:             s,
		eventBox:           s.eventBox,
		requestResponseBox: s.requestResponseBox,
		doneCallback:       doneCallback,
		channels:           make(map[string]*ChatChannel),
	}

	var ok bool
	cs.requestBoxHandle, ok = s.requestBox.Subscribe(cs.GetID())

	if !ok {
		return nil, status.Error(codes.Internal, "failed to get request box handle")
	}

	return cs, nil
}

func (cs *ChatStream) RelativeID(id string) string {
	base := cs.baseID
	base.Path = id
	return base.String()
}

func (cs *ChatStream) LookupChannel(id string) *ChatChannel {
	cs.channelLock.RLock()
	defer cs.channelLock.RUnlock()

	return cs.channels[id].Clone()
}

func (cs *ChatStream) GetID() string {
	return cs.baseID.String()
}

func (cs *ChatStream) GetType() string {
	return cs.baseID.Scheme
}

func (cs *ChatStream) GetChannels() []string {
	cs.channelLock.Lock()
	defer cs.channelLock.Unlock()

	var channels []string
	for k := range cs.channels {
		channels = append(channels, k)
	}

	sort.Strings(channels)

	return channels
}

func (cs *ChatStream) Close() {
	cs.serverLock.Lock()
	defer cs.serverLock.Unlock()

	if cs.server == nil {
		return
	}

	cs.server.removeStream(cs)
	cs.requestBoxHandle.Close()
	cs.server = nil

	cs.doneCallback()
}
