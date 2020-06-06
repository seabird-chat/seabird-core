package ircx

// TODO: store all nicks by uuid and map them in outgoing seabird events rather
// than passing the nicks around directly

import (
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"gopkg.in/irc.v3"
)

type Tracker struct {
	sync.RWMutex

	isupport *ISupportTracker
	channels map[string]*ChannelState
}

func NewTracker() *Tracker {
	return &Tracker{
		isupport: NewISupportTracker(),
		channels: make(map[string]*ChannelState),
	}
}

type ChannelState struct {
	Name  string
	Topic string
	Users map[string]struct{}
}

func (t *Tracker) ListChannels() []string {
	t.RLock()
	defer t.RUnlock()

	var ret []string
	for channel := range t.channels {
		ret = append(ret, channel)
	}

	return ret
}

func (t *Tracker) GetChannel(name string) *ChannelState {
	t.RLock()
	defer t.RUnlock()

	return t.channels[name]
}
func (t *Tracker) Handle(client *irc.Client, msg *irc.Message) {
	t.isupport.Handle(client, msg)

	switch msg.Command {
	case "332":
		t.handleRplTopic(client, msg)
	case "353":
		t.handleRplNamReply(client, msg)
	case "JOIN":
		t.handleJoin(client, msg)
	case "TOPIC":
		t.handleTopic(client, msg)
	case "PART":
		t.handlePart(client, msg)
	case "KICK":
		t.handleKick(client, msg)
	case "QUIT":
		t.handleQuit(client, msg)
	case "NICK":
		t.handleNick(client, msg)
	}
}

func (t *Tracker) handleTopic(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 2 {
		return
	}

	channel := msg.Params[0]
	topic := msg.Trailing()

	t.Lock()
	defer t.Unlock()

	if _, ok := t.channels[channel]; !ok {
		// Warning: got topic for channel we don't know about
		return
	}

	t.channels[channel].Topic = topic
}

func (t *Tracker) handleRplTopic(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 3 {
		return
	}

	// client := msg.Params[0]
	channel := msg.Params[1]
	topic := msg.Trailing()

	t.Lock()
	defer t.Unlock()

	if _, ok := t.channels[channel]; !ok {
		logrus.Warn("Got TOPIC message for untracked channel")
		return
	}

	logrus.WithFields(logrus.Fields{
		"channel": channel,
		"topic":   topic,
	}).Debug("Topic set in channel")

	t.channels[channel].Topic = topic
}

func (t *Tracker) handleJoin(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 1 {
		return
	}

	user := msg.Prefix.Name
	channel := msg.Trailing()

	t.Lock()
	defer t.Unlock()

	_, ok := t.channels[channel]

	if !ok {
		if user != client.CurrentNick() {
			logrus.Warn("Got JOIN message for untracked channel")
			return
		}

		logrus.WithFields(logrus.Fields{
			"channel": channel,
		}).Debug("Tracking channel")

		t.channels[channel] = &ChannelState{Name: channel, Users: make(map[string]struct{})}
	}

	logrus.WithFields(logrus.Fields{
		"nick":    user,
		"channel": channel,
	}).Debug("User joined channel")

	state := t.channels[channel]
	state.Users[user] = struct{}{}
}

func (t *Tracker) handlePart(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) < 1 {
		return
	}

	user := msg.Prefix.Name
	channel := msg.Params[0]

	t.Lock()
	defer t.Unlock()

	if _, ok := t.channels[channel]; !ok {
		logrus.Warn("Got PART message for untracked channel")
		return
	}

	if user == client.CurrentNick() {
		logrus.WithFields(logrus.Fields{
			"channel": channel,
		}).Debug("Done tracking channel")

		delete(t.channels, channel)
	} else {
		logrus.WithFields(logrus.Fields{
			"nick":    user,
			"channel": channel,
		}).Debug("User left channel")

		state := t.channels[channel]
		delete(state.Users, user)
	}
}

func (t *Tracker) handleKick(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 3 {
		return
	}

	actor := msg.Prefix.Name
	user := msg.Params[1]
	channel := msg.Params[0]

	t.Lock()
	defer t.Unlock()

	if _, ok := t.channels[channel]; !ok {
		logrus.Warn("Got KICK message for untracked channel")
		return
	}

	logrus.WithFields(logrus.Fields{
		"actor":   actor,
		"nick":    user,
		"channel": channel,
	}).Debug("User was kicked from channel")

	if user == client.CurrentNick() {
		delete(t.channels, channel)
	} else {
		state := t.channels[channel]
		delete(state.Users, user)
	}
}

func (t *Tracker) handleQuit(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 1 {
		return
	}

	user := msg.Prefix.Name

	logrus.WithFields(logrus.Fields{
		"nick": user,
	}).Debug("User has quit")

	t.Lock()
	defer t.Unlock()

	for _, state := range t.channels {
		delete(state.Users, user)
	}
}

func (t *Tracker) handleNick(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 1 {
		return
	}

	oldUser := msg.Prefix.Name
	newUser := msg.Params[0]

	logrus.WithFields(logrus.Fields{
		"old_nick": oldUser,
		"new_nick": newUser,
	}).Debug("Renaming user")

	t.Lock()
	defer t.Unlock()

	for _, state := range t.channels {
		if _, ok := state.Users[oldUser]; ok {
			delete(state.Users, oldUser)
			state.Users[newUser] = struct{}{}
		}
	}
}

func (t *Tracker) handleRplNamReply(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 4 {
		return
	}

	channel := msg.Params[2]
	users := strings.Split(strings.TrimSpace(msg.Trailing()), " ")

	prefixes, ok := t.isupport.GetPrefixMap()
	if !ok {
		return
	}

	t.Lock()
	defer t.Unlock()

	if _, ok := t.channels[channel]; !ok {
		logrus.Warn("Got RPL_NAMREPLY message for untracked channel")
		return
	}

	for _, user := range users {
		i := strings.IndexFunc(user, func(r rune) bool {
			_, ok := prefixes[r]
			return !ok
		})

		if i != -1 {
			user = user[i:]
		}

		// The bot user should be added via JOIN
		if user == client.CurrentNick() {
			continue
		}

		state := t.channels[channel]
		state.Users[user] = struct{}{}
	}
}
