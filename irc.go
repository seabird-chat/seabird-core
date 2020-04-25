package seabird

import (
	"strings"
	"unicode"

	"github.com/sirupsen/logrus"
	irc "gopkg.in/irc.v3"

	"github.com/belak/seabird-core/pb"
)

func (s *Server) ircHandler(client *irc.Client, msg *irc.Message) {
	logger := logrus.WithField("irc_msg", msg)
	logger.Debug("Got IRC message")

	s.tracker.handleMessage(logger, client, msg)

	if msg.Command == "001" {
		_ = client.Write("JOIN #encoded-test")
		logger.Info("Connected")
	}

	event := &pb.SeabirdEvent{Event: nil}

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

			event.Event = &pb.SeabirdEvent_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
				ReplyTo: sender,
				Sender:  sender,
				Message: message,
			}}
		} else {
			if strings.HasPrefix(lastArg, s.config.CommandPrefix) {
				msgParts := strings.SplitN(lastArg, " ", 2)

				if len(msgParts) != 2 {
					return
				}

				channel := msg.Params[0]
				sender := msg.Prefix.Name
				command := strings.TrimPrefix(msgParts[0], s.config.CommandPrefix)
				arg := msgParts[1]

				logger.WithFields(logrus.Fields{
					"channel": channel,
					"sender":  sender,
					"command": command,
					"arg":     arg,
				}).Info("Generating command event")

				event.Event = &pb.SeabirdEvent_Command{Command: &pb.CommandEvent{
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

				event.Event = &pb.SeabirdEvent_Mention{Mention: &pb.MentionEvent{
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

				event.Event = &pb.SeabirdEvent_Message{Message: &pb.MessageEvent{
					ReplyTo: channel,
					Sender:  sender,
					Message: message,
				}}
			}
		}
	}

	if event.Event != nil {
		s.pluginLock.RLock()
		defer s.pluginLock.RUnlock()

		for _, plugin := range s.plugins {
			// NOTE: this is the *ONLY* portion of the code that is allowed to
			// modify consecutiveDroppedMessages. Because this isn't called
			// concurrently, even though consecutiveDroppedMessages isn't behind
			// a mutex, this is fine.
			plugin.RLock()

			for _, stream := range plugin.streams {
				// If this was a command event and this stream didn't specify it
				// supports this command, don't send it.
				if cmdEvent, ok := event.Event.(*pb.SeabirdEvent_Command); ok {
					if _, ok := stream.commands[cmdEvent.Command.Command]; !ok {
						continue
					}
				}

				select {
				case stream.broadcast <- event:
					plugin.consecutiveDroppedMessages = 0
				default:
					logger.WithField("plugin", plugin.name).Warn("Plugin dropped a message")
					plugin.consecutiveDroppedMessages++
				}
			}

			plugin.RUnlock()
		}
	}
}
