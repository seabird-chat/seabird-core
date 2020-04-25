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
					Channel: channel,
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
					Channel: channel,
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
					Channel: channel,
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
			if plugin.broadcast != nil {
				select {
				case plugin.broadcast <- event:
					plugin.droppedMessages = 0
				default:
					logger.WithField("plugin", plugin.name).Warn("Plugin dropped a message")
					plugin.droppedMessages++
				}
			}
		}
	}
}
