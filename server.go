//go:generate protoc -I ./pb --go_out=plugins=grpc:./pb/ ./pb/seabird.proto

package seabird

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"sync"
	"unicode"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/belak/seabird-core/pb"
	"github.com/sirupsen/logrus"
	irc "gopkg.in/irc.v3"
)

// TODO: store all nicks by uuid and map them in outgoing seabird events rather
// than passing the nicks around directly

// TODO: add configuration of timeouts and command prefix

const COMMAND_PREFIX = ","

type Server struct {
	client     *irc.Client
	grpcServer *grpc.Server

	pluginLock sync.RWMutex
	plugins    map[string]*pluginState

	tracker *Tracker
}

type pluginState struct {
	sync.Mutex

	name      string
	clientId  string
	broadcast chan *pb.SeabirdEvent

	// TODO: do something with this metric
	droppedMessages int
}

func NewServer() (*Server, error) {
	c, err := tls.Dial("tcp", "irc.hs.gy:9999", &tls.Config{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, err
	}

	s := &Server{
		plugins: make(map[string]*pluginState),
		tracker: NewTracker(),
	}

	client := irc.NewClient(c, irc.ClientConfig{
		Nick:    "seabird51",
		User:    "seabird",
		Name:    "Seabird Bot",
		Handler: irc.HandlerFunc(s.ircHandler),
	})

	s.client = client

	s.grpcServer = grpc.NewServer()
	pb.RegisterSeabirdServer(s.grpcServer, s)

	// TODO: properly handle this error
	go client.Run()

	return s, nil
}

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
			if strings.HasPrefix(lastArg, COMMAND_PREFIX) {
				msgParts := strings.SplitN(lastArg, " ", 2)

				if len(msgParts) != 2 {
					return
				}

				channel := msg.Params[0]
				sender := msg.Prefix.Name
				command := strings.TrimPrefix(msgParts[0], COMMAND_PREFIX)
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
					plugin.droppedMessages++
				}
			}
		}
	}
}

func (s *Server) lookupPlugin(ctx context.Context) (*pluginState, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md["client_id"]) != 1 || len(md["plugin"]) != 1 {
		return nil, status.Error(codes.Unauthenticated, "missing client_id or plugin metadata")
	}

	clientId := md["client_id"][0]
	plugin := md["plugin"][0]

	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	meta := s.plugins[plugin]

	if meta == nil || meta.clientId != clientId {
		return nil, status.Error(codes.Unauthenticated, "plugin has not been registered or invalid client_id")
	}

	return meta, nil
}

func (s *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", ":11235")
	if err != nil {
		return err
	}

	return s.grpcServer.Serve(listener)
}
