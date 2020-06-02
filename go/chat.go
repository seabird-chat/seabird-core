package seabird

import (
	"context"

	"github.com/seabird-irc/seabird-core/pb"
)

type ChatConnection interface {
	CurrentNick() string

	SendMessage(target, message string) error
	SetChannelTopic(channel, topic string) error

	JoinChannel(channel string) error
	LeaveChannel(channel string) error

	ListChannels() ([]string, error)
	GetChannelInfo(channel string) (*pb.ChannelInfoResponse, error)

	RunContext(ctx context.Context) error
}
