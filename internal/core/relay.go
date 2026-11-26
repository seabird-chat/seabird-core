package core

import (
	"context"
	"fmt"

	"github.com/belak/x/slogx"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/protobuf/proto"
)

// Backends don't say how they recognize commands and mentions, so relays
// assume the usual command prefix and bot name.
const (
	commandPrefix = "!"
	mentionPrefix = "seabird: "
)

// relayQueueSize is how many relays can wait to be sent to a backend. They're
// queued while handling another backend's events, which can't wait, so a relay
// that finds the queue full is dropped.
const relayQueueSize = 32

// relay is chat from one member of a group, on its way to another.
type relay struct {
	channelID string // the member it's for
	request   *pb.ChatRequest
}

// chatContent is chat a backend reported, ready to relay.
type chatContent struct {
	author *pb.User
	action bool
	body   *pb.Block // normalized; shared with the published event
	tags   map[string]string
}

// relayChat copies chat from a channel to the other members of its group, if
// it's in one. Members on disconnected backends are skipped.
func (s *Server) relayChat(ctx context.Context, from *backend, channel *Channel, content chatContent) error {
	if channel == nil || channel.GroupID == nil {
		return nil
	}

	members, err := s.DB.ListGroupMembers(ctx, *channel.GroupID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if member.ID == channel.ID {
			continue
		}
		target := s.backends.get(member.BackendID)
		if target == nil {
			continue
		}
		s.queueRelay(ctx, target, relay{
			channelID: member.ID,
			request:   relayRequest(target, member, from.row, content),
		})
	}
	return nil
}

// relayRequest is what target is asked to send to member. Backends that show
// authors themselves are told who it's from, and the rest get the author in
// the message.
func relayRequest(target *backend, member Channel, from *Backend, content chatContent) *pb.ChatRequest {
	if target.features[pb.BackendFeature_BACKEND_FEATURE_AUTHOR] {
		// Backends never see seabird IDs, so only what describes the author
		// is passed on.
		author := &pb.User{DisplayName: content.author.GetDisplayName()}
		authorBackend := &pb.Backend{Type: from.Type}

		if content.action {
			return &pb.ChatRequest{Inner: &pb.ChatRequest_PerformAction{PerformAction: &pb.PerformActionChatRequest{
				ChannelId:     member.ExternalID,
				Text:          content.body.GetPlain(),
				RootBlock:     content.body,
				Tags:          content.tags,
				Author:        author,
				AuthorBackend: authorBackend,
			}}}
		}
		return &pb.ChatRequest{Inner: &pb.ChatRequest_SendMessage{SendMessage: &pb.SendMessageChatRequest{
			ChannelId:     member.ExternalID,
			Text:          content.body.GetPlain(),
			RootBlock:     content.body,
			Tags:          content.tags,
			Author:        author,
			AuthorBackend: authorBackend,
		}}}
	}

	block := withAuthor(content, from.Type)
	return &pb.ChatRequest{Inner: &pb.ChatRequest_SendMessage{SendMessage: &pb.SendMessageChatRequest{
		ChannelId: member.ExternalID,
		Text:      block.GetPlain(),
		RootBlock: block,
		Tags:      content.tags,
	}}}
}

// withAuthor puts the author ahead of content's body, as "alice (irc): hi", or
// "* alice (irc) waves" for actions, with the author in bold.
func withAuthor(content chatContent, backendType string) *pb.Block {
	lead, tail := "", ": "
	if content.action {
		lead, tail = "* ", " "
	}
	label := fmt.Sprintf("%s (%s)", content.author.GetDisplayName(), backendType)

	// The body is shared with the published event, so it's copied before
	// its plain text is rendered again.
	var parts []*pb.Block
	if lead != "" {
		parts = append(parts, textBlock(lead))
	}
	parts = append(parts,
		&pb.Block{Inner: &pb.Block_Bold{Bold: &pb.BoldBlock{Inner: textBlock(label)}}},
		textBlock(tail),
		proto.Clone(content.body).(*pb.Block),
	)
	block := &pb.Block{Inner: &pb.Block_Container{Container: &pb.ContainerBlock{Inner: parts}}}

	// Every part is valid, so this can't fail.
	block, _ = normalizeBlock("", block)
	return block
}

// mentionBody is a mention as it was typed, assuming the bot's name.
func mentionBody(block *pb.Block) *pb.Block {
	body := &pb.Block{Inner: &pb.Block_Container{Container: &pb.ContainerBlock{
		Inner: []*pb.Block{textBlock(mentionPrefix), proto.Clone(block).(*pb.Block)},
	}}}

	// The mention was already normalized, so this can't fail.
	body, _ = normalizeBlock("", body)
	return body
}

// commandBody is a command as it was typed, assuming the command prefix.
func commandBody(command *pb.CommandEvent) *pb.Block {
	text := commandPrefix + command.GetCommand()
	if command.GetArg() != "" {
		text += " " + command.GetArg()
	}
	body, _ := normalizeBlock(text, nil)
	return body
}

func textBlock(text string) *pb.Block {
	return &pb.Block{Inner: &pb.Block_Text{Text: &pb.TextBlock{Text: text}}}
}

// queueRelay hands r to target's relay worker, dropping it if the queue is
// full.
func (s *Server) queueRelay(ctx context.Context, target *backend, r relay) {
	select {
	case target.relays <- r:
	default:
		slogx.FromContext(ctx).WarnContext(ctx, "relay queue full, dropping relay",
			slogx.String("channel_id", r.channelID))
	}
}

// runRelays sends b the relays queued for it, one at a time so they arrive in
// order, until ctx is done.
func (s *Server) runRelays(ctx context.Context, b *backend) {
	logger := slogx.FromContext(ctx)
	for {
		select {
		case r := <-b.relays:
			reply, err := s.issueRequest(ctx, b, r.request)
			if err == nil && reply.GetSuccess() == nil {
				err = replyError(reply)
			}
			if err != nil {
				logger.WarnContext(ctx, "failed to relay to channel group member",
					slogx.String("channel_id", r.channelID), slogx.Err(err))
			}
		case <-ctx.Done():
			return
		}
	}
}
