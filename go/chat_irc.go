package seabird

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-irc/irc/v4"
	"github.com/go-irc/ircx"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/seabird-irc/seabird-core/pb"
)

type IRCConn struct {
	sync.RWMutex

	client             *ircx.Client
	broadcastEvent     func(*pb.Event)
	getCommandMetadata func() map[string][]*CommandMetadata

	// TODO: find a way to remove this from the struct
	config *ServerConfig
}

func NewIRCConn(
	config *ServerConfig,
	ircUrl *url.URL,
	broadcastEvent func(*pb.Event),
	getCommandMetadata func() map[string][]*CommandMetadata,
) (*IRCConn, error) {
	var err error

	hostname := ircUrl.Hostname()
	port := ircUrl.Port()

	var conn io.ReadWriteCloser

	switch ircUrl.Scheme {
	case "irc":
		if port == "" {
			port = "6667"
		}

		conn, err = net.Dial("tcp", fmt.Sprintf("%s:%s", hostname, port))
	case "ircs":
		if port == "" {
			port = "6697"
		}

		conn, err = tls.Dial("tcp", fmt.Sprintf("%s:%s", hostname, port), nil)
	case "ircs+unsafe":
		if port == "" {
			port = "6697"
		}

		conn, err = tls.Dial("tcp", fmt.Sprintf("%s:%s", hostname, port), &tls.Config{
			InsecureSkipVerify: true,
		})
	default:
		return nil, fmt.Errorf("unknown irc scheme %s", ircUrl.Scheme)
	}

	if err != nil {
		return nil, err
	}

	c := &IRCConn{
		broadcastEvent:     broadcastEvent,
		getCommandMetadata: getCommandMetadata,
		config:             config,
	}

	c.client = ircx.NewClient(conn, ircx.ClientConfig{
		// Connection info
		Nick: config.Nick,
		User: config.User,
		Name: config.Name,
		Pass: config.Pass,

		// We need to enable the tracker so we can know what channels we're in.
		EnableISupport: true,
		EnableTracker:  true,

		// Various internal details.
		PingFrequency: 60 * time.Second,
		PingTimeout:   10 * time.Second,
		Handler:       ircx.HandlerFunc(c.handler),
	})

	return c, nil
}

func (c *IRCConn) CurrentNick() string {
	return c.client.CurrentNick()
}

func (c *IRCConn) SendMessage(target, message string) error {
	err := c.client.WriteMessage(&irc.Message{
		Command: "PRIVMSG",
		Params:  []string{target, message},
	})
	if err != nil {
		return status.Error(codes.Internal, "failed to write message")
	}
	return nil
}

func (c *IRCConn) JoinChannel(channel string) error {
	err := c.client.WriteMessage(&irc.Message{
		Command: "JOIN",
		Params:  []string{channel},
	})
	if err != nil {
		return status.Error(codes.Internal, "failed to write message")
	}
	return nil
}

func (c *IRCConn) LeaveChannel(channel string) error {
	err := c.client.WriteMessage(&irc.Message{
		Command: "PART",
		Params:  []string{channel},
	})
	if err != nil {
		return status.Error(codes.Internal, "failed to write message")
	}
	return nil
}

func (c *IRCConn) ListChannels() ([]string, error) {
	return c.client.Tracker.ListChannels(), nil
}

func (c *IRCConn) GetChannelInfo(name string) (*pb.ChannelInfoResponse, error) {
	channel := c.client.Tracker.GetChannel(name)
	if channel == nil {
		return nil, status.Error(codes.InvalidArgument, "unknown channel")
	}

	users := make([]*pb.User, len(channel.Users))
	for nick := range channel.Users {
		users = append(users, &pb.User{Nick: nick})
	}

	return &pb.ChannelInfoResponse{
		Name:  channel.Name,
		Topic: channel.Topic,
		Users: users,
	}, nil
}

func (c *IRCConn) RunContext(ctx context.Context) error {
	return c.client.RunContext(ctx)
}

func (c *IRCConn) SetChannelTopic(channel, topic string) error {
	c.RLock()
	defer c.RUnlock()

	if c.client.Tracker.GetChannel(channel) == nil {
		return status.Error(codes.InvalidArgument, "unknown channel")
	}

	err := c.client.WriteMessage(&irc.Message{
		Command: "TOPIC",
		Params:  []string{channel, topic},
	})
	if err != nil {
		return status.Error(codes.Internal, "failed to write message")
	}
	return nil
}

func (c *IRCConn) handler(client *ircx.Client, msg *irc.Message) {
	logger := logrus.WithField("irc_msg", msg)
	logger.Debug("Got IRC message")

	if msg.Command == "001" {
		_ = client.Write("JOIN #encoded-test")
		logger.Info("Connected")
	}

	event := &pb.Event{Inner: nil}

	if msg.Command == "PRIVMSG" && len(msg.Params) == 2 {
		lastArg := msg.Trailing()
		currentNick := c.client.CurrentNick()

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
			if strings.HasPrefix(lastArg, c.config.CommandPrefix) {
				msgParts := strings.SplitN(lastArg, " ", 2)
				if len(msgParts) < 2 {
					msgParts = append(msgParts, "")
				}

				channel := msg.Params[0]
				sender := msg.Prefix.Name
				command := strings.TrimPrefix(msgParts[0], c.config.CommandPrefix)
				arg := msgParts[1]

				// If we were issued a help command, handle it internally and
				// then dispatch it externally just in case someone wants to
				// watch for stats.
				if command == "help" {
					c.handleHelp(channel, sender, arg)
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
		c.broadcastEvent(event)
	}
}

func (c *IRCConn) handleHelp(channel, sender, arg string) {
	pluginMeta := c.getCommandMetadata()

	// If an arg was given, look up that command, otherwise give a list of commands
	arg = strings.TrimSpace(arg)
	if arg == "" {
		commands := make([]string, len(pluginMeta))
		for name := range pluginMeta {
			commands = append(commands, name)
		}
		sort.Strings(commands)

		err := c.client.Writef("PRIVMSG %s :%s: Available commands: %s", channel, sender, strings.Join(commands, ", "))
		if err != nil {
			logrus.WithError(err).Error("Failed to write message")
		}
	} else {
		if metas, ok := pluginMeta[arg]; ok {
			for _, meta := range metas {
				err := c.client.Writef("PRIVMSG %s :%s: %s: %s", channel, sender, arg, meta.ShortHelp)
				if err != nil {
					logrus.WithError(err).Error("Failed to write message")
				}
			}
		} else {
			err := c.client.Writef("PRIVMSG %s :%s: Unknown command %q", channel, sender, arg)
			if err != nil {
				logrus.WithError(err).Error("Failed to write message")
			}
		}
	}
}
