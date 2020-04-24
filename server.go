//go:generate protoc -I ./pb --go_out=plugins=grpc:./pb/ ./pb/seabird.proto

package seabird

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/belak/seabird-core/pb"
	irc "gopkg.in/irc.v3"
)

// TODO: store all nicks by uuid and map them in outgoing seabird events

const COMMAND_PREFIX = ","

type Server struct {
	client *irc.Client

	pluginLock sync.RWMutex
	plugins    map[string]*pluginState

	chanLock sync.RWMutex
	channels map[string]*channelState

	isupportLock sync.RWMutex
	isupport     map[string]string
}

type pluginState struct {
	sync.Mutex

	name      string
	clientId  string
	broadcast chan *pb.SeabirdEvent

	// TODO: do something with this metric
	droppedMessages int
}

type channelState struct {
	name  string
	topic string
	users map[string]struct{}
}

func NewServer() (*Server, error) {
	c, err := tls.Dial("tcp", "irc.hs.gy:9999", &tls.Config{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, err
	}

	s := &Server{
		plugins:  make(map[string]*pluginState),
		channels: make(map[string]*channelState),
		isupport: make(map[string]string),
	}

	// Set sane ISUPPORT defaults
	s.isupport["PREFIX"] = "(ov)@+"

	client := irc.NewClient(c, irc.ClientConfig{
		Nick:    "seabird51",
		User:    "seabird",
		Name:    "Seabird Bot",
		Handler: irc.HandlerFunc(s.ircHandler),
	})

	s.client = client

	// TODO: properly handle this error
	go client.Run()

	return s, nil
}

func (s *Server) ircHandler(client *irc.Client, msg *irc.Message) {
	if msg.Command == "001" {
		_ = client.Write("JOIN #encoded-test")
	} else if msg.Command == "005" {
		s.handleISupport(msg)
	} else if msg.Command == "332" {
		s.handleRplTopic(client, msg)
	} else if msg.Command == "353" {
		s.handleRplNamReply(client, msg)
	} else if msg.Command == "JOIN" {
		s.handleJoin(client, msg)
	} else if msg.Command == "TOPIC" {
		s.handleTopic(client, msg)
	} else if msg.Command == "PART" {
		s.handlePart(client, msg)
	} else if msg.Command == "KICK" {
		s.handleKick(client, msg)
	} else if msg.Command == "QUIT" {
		s.handleQuit(client, msg)
	} else if msg.Command == "NICK" {
		s.handleNick(client, msg)
	}

	event := &pb.SeabirdEvent{Event: nil}

	if msg.Command == "PRIVMSG" && len(msg.Params) == 2 {
		lastArg := msg.Trailing()
		currentNick := s.client.CurrentNick()

		if msg.Params[0] == currentNick {
			event.Event = &pb.SeabirdEvent_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
				Sender:  msg.Prefix.Name,
				Message: lastArg,
			}}
		} else {
			if strings.HasPrefix(lastArg, COMMAND_PREFIX) {
				msgParts := strings.SplitN(lastArg, " ", 2)

				if len(msgParts) == 2 {
					event.Event = &pb.SeabirdEvent_Command{Command: &pb.CommandEvent{
						Channel: msg.Params[0],
						Sender:  msg.Prefix.Name,
						Command: strings.TrimPrefix(msgParts[0], COMMAND_PREFIX),
						Arg:     msgParts[1],
					}}
				}
			} else if len(lastArg) >= len(currentNick)+1 &&
				strings.HasPrefix(lastArg, currentNick) &&
				unicode.IsPunct(rune(lastArg[len(currentNick)])) &&
				lastArg[len(currentNick)+1] == ' ' {
				event.Event = &pb.SeabirdEvent_Message{Message: &pb.MessageEvent{
					Channel: msg.Params[0],
					Sender:  msg.Prefix.Name,
					Message: strings.TrimSpace(lastArg[len(currentNick)+1:]),
				}}
			} else {
				event.Event = &pb.SeabirdEvent_Message{Message: &pb.MessageEvent{
					Channel: msg.Params[0],
					Sender:  msg.Prefix.Name,
					Message: msg.Params[1],
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
				default:
					plugin.droppedMessages++
				}
			}
		}
	}
}

func (s *Server) lookupPlugin(ctx context.Context) (*pluginState, error) {
	md, ok := metadata.FromIncomingContext(ctx) // get context from stream
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
		log.Fatalf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterSeabirdServer(grpcServer, s)
	return grpcServer.Serve(listener)
}

// Register is the internal implementation of SeabirdServer.Register
func (s *Server) Register(ctx context.Context, in *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	clientId := uuid.New().String()
	plugin := in.Identifier

	s.pluginLock.Lock()
	defer s.pluginLock.Unlock()

	if _, ok := s.plugins[plugin]; ok {
		return nil, status.Error(codes.PermissionDenied, "plugin already registered")
	}

	state := &pluginState{name: plugin, clientId: clientId}

	s.plugins[plugin] = state

	// Clean up any plugins which fail to register within 5 seconds
	defer func() {
		go func() {
			time.Sleep(5 * time.Second)

			state.Lock()
			defer state.Unlock()

			if state.broadcast == nil {
				s.pluginLock.Lock()
				defer s.pluginLock.Unlock()

				delete(s.plugins, state.name)
			}
		}()
	}()

	return &pb.RegisterResponse{ClientId: clientId}, nil
}

// EventStream is the internal implementation of SeabirdServer.EventStream
func (s *Server) EventStream(in *pb.EventStreamRequest, stream pb.Seabird_EventStreamServer) error {
	ctx := stream.Context()
	plugin, err := s.lookupPlugin(ctx)
	if err != nil {
		return err
	}

	plugin.Lock()

	if plugin.broadcast != nil {
		plugin.Unlock()
		return status.Error(codes.PermissionDenied, "plugin event stream already started")
	}

	plugin.broadcast = make(chan *pb.SeabirdEvent)
	plugin.Unlock()

	// Ensure we properly clean up the plugin information when a plugin's event stream dies.
	defer func() {
		s.pluginLock.Lock()
		defer s.pluginLock.Unlock()

		delete(s.plugins, plugin.name)
	}()

	for {
		select {
		case event := <-plugin.broadcast:
			err = stream.Send(event)
			if err != nil {
				return status.Error(codes.Internal, "failed to send event")
			}
		case <-ctx.Done():
			return status.Error(codes.Canceled, "event stream cancelled")
		}
	}
}

// SendMessage is the internal implementation of SeabirdServer.SendMessage
func (s *Server) SendMessage(ctx context.Context, req *pb.SendMessageRequest) (*pb.SendMessageResponse, error) {
	_, err := s.lookupPlugin(ctx)
	if err != nil {
		return nil, err
	}

	err = s.client.WriteMessage(&irc.Message{
		Command: "PRIVMSG",
		Params:  []string{req.Target, req.Message},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.SendMessageResponse{}, nil
}

// SendReplyMessage is the internal implementation of SeabirdServer.SendReplyMessage
func (s *Server) SendReplyMessage(ctx context.Context, req *pb.SendReplyMessageRequest) (*pb.SendReplyMessageResponse, error) {
	_, err := s.lookupPlugin(ctx)
	if err != nil {
		return nil, err
	}

	switch event := req.Target.Event.(type) {
	case (*pb.SeabirdEvent_Message):
		if req.Mention {
			err = s.client.Writef("PRIVMSG %s :%s: %s", event.Message.Channel, event.Message.Sender, req.Message)
		} else {
			err = s.client.Writef("PRIVMSG %s :%s", event.Message.Channel, req.Message)
		}
	case (*pb.SeabirdEvent_Command):
		if req.Mention {
			err = s.client.Writef("PRIVMSG %s :%s: %s", event.Command.Channel, event.Command.Sender, req.Message)
		} else {
			err = s.client.Writef("PRIVMSG %s :%s", event.Command.Channel, req.Message)
		}
	case (*pb.SeabirdEvent_Mention):
		if req.Mention {
			err = s.client.Writef("PRIVMSG %s :%s: %s", event.Mention.Channel, event.Mention.Sender, req.Message)
		} else {
			err = s.client.Writef("PRIVMSG %s :%s", event.Mention.Channel, req.Message)
		}
	case (*pb.SeabirdEvent_PrivateMessage):
		err = s.client.Writef("PRIVMSG %s :%s", event.PrivateMessage.Sender, req.Message)
	default:
		return nil, status.Error(codes.Internal, "unknown event type")
	}

	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return &pb.SendReplyMessageResponse{}, nil
}

// GetChannel is the internal implementation of SeabirdServer.GetChannel
func (s *Server) GetChannel(ctx context.Context, req *pb.ChannelRequest) (*pb.ChannelResponse, error) {
	_, err := s.lookupPlugin(ctx)
	if err != nil {
		return nil, err
	}

	s.chanLock.RLock()
	defer s.chanLock.RUnlock()

	channel, ok := s.channels[strings.ToLower(req.Name)]
	if !ok {
		return nil, status.Error(codes.NotFound, "channel not found")
	}

	resp := &pb.ChannelResponse{Name: channel.name, Topic: channel.topic, Users: nil}
	for nick := range channel.users {
		resp.Users = append(resp.Users, &pb.User{Nick: nick})
	}

	return resp, nil
}

// JoinChannel is the internal implementation of SeabirdServer.JoinChannel
func (s *Server) JoinChannel(ctx context.Context, req *pb.JoinChannelRequest) (*pb.JoinChannelResponse, error) {
	_, err := s.lookupPlugin(ctx)
	if err != nil {
		return nil, err
	}

	err = s.client.WriteMessage(&irc.Message{
		Command: "JOIN",
		Params:  []string{req.Target},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return nil, status.Error(codes.Unimplemented, "JoinChannel unimplemented")
}

// LeaveChannel is the internal implementation of SeabirdServer.LeaveChannel
func (s *Server) LeaveChannel(ctx context.Context, req *pb.LeaveChannelRequest) (*pb.LeaveChannelResponse, error) {
	_, err := s.lookupPlugin(ctx)
	if err != nil {
		return nil, err
	}

	err = s.client.WriteMessage(&irc.Message{
		Command: "PART",
		Params:  []string{req.Target, req.Message},
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to write message")
	}

	return nil, status.Error(codes.Unimplemented, "LeaveChannel unimplemented")
}
