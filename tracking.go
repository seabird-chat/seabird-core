package seabird

import (
	"strings"

	"gopkg.in/irc.v3"
)

func (s *Server) handleTopic(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 2 {
		return
	}

	channel := msg.Params[0]
	topic := msg.Trailing()

	s.chanLock.Lock()
	defer s.chanLock.Unlock()

	if _, ok := s.channels[channel]; !ok {
		// Warning: got topic for channel we don't know about
		return
	}

	s.channels[channel].topic = topic
}

func (s *Server) handleRplTopic(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 3 {
		return
	}

	// client := msg.Params[0]
	channel := msg.Params[1]
	topic := msg.Trailing()

	s.chanLock.Lock()
	defer s.chanLock.Unlock()

	if _, ok := s.channels[channel]; !ok {
		// Warning: got topic for channel we don't know about
		return
	}

	s.channels[channel].topic = topic
}

func (s *Server) handleJoin(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 1 {
		return
	}

	user := msg.Prefix.Name
	channel := msg.Trailing()

	s.chanLock.Lock()
	defer s.chanLock.Unlock()

	_, ok := s.channels[channel]

	if !ok {
		if user == client.CurrentNick() {
			s.channels[channel] = &channelState{name: channel, users: make(map[string]struct{})}
		} else {
			// Warning: got join for channel we don't know about
			return
		}
	}

	state := s.channels[channel]
	state.users[user] = struct{}{}
}

func (s *Server) handlePart(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 2 {
		return
	}

	user := msg.Prefix.Name
	channel := msg.Params[0]

	s.chanLock.Lock()
	defer s.chanLock.Unlock()

	if _, ok := s.channels[channel]; !ok {
		// Warning: got part for channel we don't know about
		return
	}

	if user == client.CurrentNick() {
		delete(s.channels, channel)
	} else {
		state := s.channels[channel]
		delete(state.users, user)
	}
}

func (s *Server) handleKick(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 3 {
		return
	}

	//actor := m.Prefix.Name
	user := msg.Params[1]
	channel := msg.Params[0]

	s.chanLock.Lock()
	defer s.chanLock.Unlock()

	if _, ok := s.channels[channel]; !ok {
		// Warning: got kick for channel we don't know about
		return
	}

	if user == client.CurrentNick() {
		delete(s.channels, channel)
	} else {
		state := s.channels[channel]
		delete(state.users, user)
	}
}

func (s *Server) handleQuit(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 1 {
		return
	}

	user := msg.Prefix.Name

	s.chanLock.Lock()
	defer s.chanLock.Unlock()

	for _, state := range s.channels {
		delete(state.users, user)
	}
}

func (s *Server) handleNick(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 1 {
		return
	}

	oldUser := msg.Prefix.Name
	newUser := msg.Params[0]

	s.chanLock.Lock()
	defer s.chanLock.Unlock()

	for _, state := range s.channels {
		if _, ok := state.users[oldUser]; ok {
			delete(state.users, oldUser)
			state.users[newUser] = struct{}{}
		}
	}
}

func (s *Server) handleRplNamReply(client *irc.Client, msg *irc.Message) {
	if len(msg.Params) != 4 {
		return
	}

	channel := msg.Params[2]
	users := strings.Split(strings.TrimSpace(msg.Trailing()), " ")

	prefixes, ok := s.isupportPrefixMap()
	if !ok {
		return
	}

	s.chanLock.Lock()
	defer s.chanLock.Unlock()

	if _, ok := s.channels[channel]; !ok {
		// Warning: got name info for channel we don't know about
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

		state := s.channels[channel]
		state.users[user] = struct{}{}
	}
}
