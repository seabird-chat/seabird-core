package seabird

import (
	"sort"
	"strings"
	"unicode"

	"github.com/sirupsen/logrus"
	irc "gopkg.in/irc.v3"

	"github.com/seabird-irc/seabird-core/pb"
)

func (s *Server) ircHandler(client *irc.Client, msg *irc.Message) {
	logger := logrus.WithField("irc_msg", msg)
	logger.Debug("Got IRC message")

	s.tracker.Handle(client, msg)

	if msg.Command == "001" {
		_ = client.Write("JOIN #encoded-test")
		logger.Info("Connected")
	}

	event := &pb.Event{Inner: nil}

	if msg.Command == "PRIVMSG" && len(msg.Params) == 2 {
		lastArg := msg.Trailing()
		currentNick := s.client.CurrentNick()

		if msg.Params[0] == currentNick {
			sender := msg.Prefix.Name
			message := lastArg

			logger.WithFields(logrus.Fields{
				"sender":  sender,
				"message": message,
			}).Info("Generating private message event")

			event.Inner = &pb.Event_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
				ReplyTo: sender,
				Sender:  sender,
				Message: message,
			}}
		} else {
			if strings.HasPrefix(lastArg, s.config.CommandPrefix) {
				msgParts := strings.SplitN(lastArg, " ", 2)
				if len(msgParts) < 2 {
					msgParts = append(msgParts, "")
				}

				channel := msg.Params[0]
				sender := msg.Prefix.Name
				command := strings.TrimPrefix(msgParts[0], s.config.CommandPrefix)
				arg := msgParts[1]

				// If we were issued a help command, handle it internally and
				// then dispatch it externally just in case someone wants to
				// watch for stats.
				if command == "help" {
					s.handleHelp(channel, sender, arg)
				}

				logger.WithFields(logrus.Fields{
					"channel": channel,
					"sender":  sender,
					"command": command,
					"arg":     arg,
				}).Info("Generating command event")

				event.Inner = &pb.Event_Command{Command: &pb.CommandEvent{
					ReplyTo: channel,
					Sender:  sender,
					Command: command,
					Arg:     arg,
				}}

			} else if len(lastArg) >= len(currentNick)+1 &&
				strings.HasPrefix(lastArg, currentNick) &&
				unicode.IsPunct(rune(lastArg[len(currentNick)])) &&
				lastArg[len(currentNick)+1] == ' ' {
				channel := msg.Params[0]
				sender := msg.Prefix.Name
				message := strings.TrimSpace(lastArg[len(currentNick)+1:])

				logger.WithFields(logrus.Fields{
					"channel": channel,
					"sender":  sender,
					"message": message,
				}).Info("Generating mention event")

				event.Inner = &pb.Event_Mention{Mention: &pb.MentionEvent{
					ReplyTo: channel,
					Sender:  sender,
					Message: message,
				}}
			} else {
				channel := msg.Params[0]
				sender := msg.Prefix.Name
				message := lastArg

				logger.WithFields(logrus.Fields{
					"channel": channel,
					"sender":  sender,
					"message": message,
				}).Info("Generating message event")

				event.Inner = &pb.Event_Message{Message: &pb.MessageEvent{
					ReplyTo: channel,
					Sender:  sender,
					Message: message,
				}}
			}
		}
	}

	if event.Inner != nil {
		// NOTE: we get a full lock and not an RLock because there's a chance we
		// will need to remove plugins during this iteration.
		s.streamLock.Lock()
		defer s.streamLock.Unlock()

		for id, stream := range s.streams {
			// If a broadcast to this plugin fails, kill the plugin
			err := stream.Send(event)
			if err != nil {
				stream.Close()
				delete(s.streams, id)
			}
		}
	}
}

func (s *Server) getHelp() ([]string, map[string][]*CommandMetadata) {
	// Pull the help info from the cache, regenerate if it doesn't exist
	s.streamLock.RLock()
	defer s.streamLock.RUnlock()

	meta := make(map[string][]*CommandMetadata)
	commands := []string{}

	for _, stream := range s.streams {
		for _, command := range stream.Commands() {
			commandMeta := stream.CommandInfo(command)
			if meta == nil {
				continue
			}

			// If this is our first time seeing this command, add it to the
			// overall list.
			if _, ok := meta[command]; !ok {
				commands = append(commands, command)
			}

			meta[command] = append(meta[command], commandMeta)
		}
	}

	sort.Strings(commands)

	return commands, meta
}

func (s *Server) handleHelp(channel, sender, arg string) {
	commands, pluginMeta := s.getHelp()

	// If an arg was given, look up that command, otherwise give a list of commands
	arg = strings.TrimSpace(arg)
	if arg == "" {
		err := s.client.Writef("PRIVMSG %s :%s: Available commands: %s", channel, sender, strings.Join(commands, ", "))
		if err != nil {
			logrus.WithError(err).Error("Failed to write message")
		}
	} else {
		if metas, ok := pluginMeta[arg]; ok {
			for _, meta := range metas {
				err := s.client.Writef("PRIVMSG %s :%s: %s: %s", channel, sender, arg, meta.ShortHelp)
				if err != nil {
					logrus.WithError(err).Error("Failed to write message")
				}
			}
		} else {
			err := s.client.Writef("PRIVMSG %s :%s: Unknown command %q", channel, sender, arg)
			if err != nil {
				logrus.WithError(err).Error("Failed to write message")
			}
		}
	}
}
