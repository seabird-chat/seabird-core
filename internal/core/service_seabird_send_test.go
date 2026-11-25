package core

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
	seabird "github.com/seabird-chat/seabird-go"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
)

// sendRPC describes one of the four RPCs that send chat on a plugin's behalf.
type sendRPC struct {
	name    string
	target  string // an ID the plugin can send to
	path    string // what the backend should be told
	idField string // the name used when the ID is malformed
	call    func(ctx context.Context, c pb.SeabirdClient, target, text string, block *pb.Block, tags map[string]string) error

	// request and event pull the common fields out of what the backend and the
	// plugins receive. They report false for the wrong kind of message.
	request func(*pb.ChatRequest) (path, text string, block *pb.Block, tags map[string]string, ok bool)
	event   func(*pb.Event) (sender, target, text string, block *pb.Block, ok bool)
}

var sendRPCs = []sendRPC{
	{
		name:    "SendMessage",
		target:  "irc://libera/%23chan",
		path:    "#chan",
		idField: "channel_id",
		call: func(ctx context.Context, c pb.SeabirdClient, target, text string, block *pb.Block, tags map[string]string) error {
			_, err := c.SendMessage(ctx, &pb.SendMessageRequest{ChannelId: target, Text: text, RootBlock: block, Tags: tags})
			return err
		},
		request: func(r *pb.ChatRequest) (string, string, *pb.Block, map[string]string, bool) {
			m := r.GetSendMessage()
			return m.GetChannelId(), m.GetText(), m.GetRootBlock(), m.GetTags(), m != nil
		},
		event: func(e *pb.Event) (string, string, string, *pb.Block, bool) {
			m := e.GetSendMessage()
			return m.GetSender(), m.GetChannelId(), m.GetText(), m.GetRootBlock(), m != nil
		},
	},
	{
		name:    "SendPrivateMessage",
		target:  "irc://libera/alice%20smith",
		path:    "alice smith",
		idField: "user_id",
		call: func(ctx context.Context, c pb.SeabirdClient, target, text string, block *pb.Block, tags map[string]string) error {
			_, err := c.SendPrivateMessage(ctx, &pb.SendPrivateMessageRequest{UserId: target, Text: text, RootBlock: block, Tags: tags})
			return err
		},
		request: func(r *pb.ChatRequest) (string, string, *pb.Block, map[string]string, bool) {
			m := r.GetSendPrivateMessage()
			return m.GetUserId(), m.GetText(), m.GetRootBlock(), m.GetTags(), m != nil
		},
		event: func(e *pb.Event) (string, string, string, *pb.Block, bool) {
			m := e.GetSendPrivateMessage()
			return m.GetSender(), m.GetUserId(), m.GetText(), m.GetRootBlock(), m != nil
		},
	},
	{
		name:    "PerformAction",
		target:  "irc://libera/%23chan",
		path:    "#chan",
		idField: "channel_id",
		call: func(ctx context.Context, c pb.SeabirdClient, target, text string, block *pb.Block, tags map[string]string) error {
			_, err := c.PerformAction(ctx, &pb.PerformActionRequest{ChannelId: target, Text: text, RootBlock: block, Tags: tags})
			return err
		},
		request: func(r *pb.ChatRequest) (string, string, *pb.Block, map[string]string, bool) {
			m := r.GetPerformAction()
			return m.GetChannelId(), m.GetText(), m.GetRootBlock(), m.GetTags(), m != nil
		},
		event: func(e *pb.Event) (string, string, string, *pb.Block, bool) {
			m := e.GetPerformAction()
			return m.GetSender(), m.GetChannelId(), m.GetText(), m.GetRootBlock(), m != nil
		},
	},
	{
		name:    "PerformPrivateAction",
		target:  "irc://libera/alice%20smith",
		path:    "alice smith",
		idField: "user_id",
		call: func(ctx context.Context, c pb.SeabirdClient, target, text string, block *pb.Block, tags map[string]string) error {
			_, err := c.PerformPrivateAction(ctx, &pb.PerformPrivateActionRequest{UserId: target, Text: text, RootBlock: block, Tags: tags})
			return err
		},
		request: func(r *pb.ChatRequest) (string, string, *pb.Block, map[string]string, bool) {
			m := r.GetPerformPrivateAction()
			return m.GetUserId(), m.GetText(), m.GetRootBlock(), m.GetTags(), m != nil
		},
		event: func(e *pb.Event) (string, string, string, *pb.Block, bool) {
			m := e.GetPerformPrivateAction()
			return m.GetSender(), m.GetUserId(), m.GetText(), m.GetRootBlock(), m != nil
		},
	},
}

func TestSendRPCsForwardRequests(t *testing.T) {
	t.Parallel()

	variants := []struct {
		name     string
		text     string
		block    func() *pb.Block
		wantText string
	}{
		{name: "text only", text: "hello", wantText: "hello"},
		{
			name:     "blocks",
			text:     "ignored",
			block:    func() *pb.Block { return seabird.NewBoldBlock(seabird.NewTextBlock("hi")) },
			wantText: "hi",
		},
	}

	for _, rpc := range sendRPCs {
		for _, variant := range variants {
			t.Run(rpc.name+"/"+variant.name, func(t *testing.T) {
				t.Parallel()

				env := startTestEnv(t)

				var block *pb.Block
				if variant.block != nil {
					block = variant.block()
				}
				tags := map[string]string{"plugin/tag": "x"}
				result := callAsync(func() error {
					return rpc.call(env.ctx, env.plugin, rpc.target, variant.text, block, tags)
				})

				// The backend is asked to carry it out, with its own ID for the target.
				request, err := env.backend.recv()
				assert.NoError(t, err)
				path, text, requestBlock, requestTags, ok := rpc.request(request)
				assert.True(t, ok, "wrong kind of request")
				assert.Equal(t, rpc.path, path)
				assert.Equal(t, variant.wantText, text)
				assert.Equal(t, variant.wantText, requestBlock.GetPlain())
				wantTags := map[string]string{"plugin/tag": "x"}
				assert.Equal(t, wantTags, requestTags)

				env.backend.reply(request, successEvent(""))
				assert.NoError(t, waitFor(t, result, "result"))

				// And plugins are told about it, with the ID the caller used.
				event := env.events.next(t)
				sender, target, eventText, eventBlock, ok := rpc.event(event)
				assert.True(t, ok, "wrong kind of event")
				assert.Equal(t, testUsername, sender)
				assert.Equal(t, rpc.target, target)
				assert.Equal(t, variant.wantText, eventText)
				assertProtoEqual(t, requestBlock, eventBlock)
				assert.Equal(t, wantTags, event.GetTags())
			})
		}
	}
}

func TestSendRPCsReportFailures(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	rpc := sendRPCs[0]

	result := callAsync(func() error { return rpc.call(env.ctx, env.plugin, rpc.target, "hi", nil, nil) })
	request, err := env.backend.recv()
	assert.NoError(t, err)
	env.backend.reply(request, &pb.ChatEvent{Inner: &pb.ChatEvent_Failed{
		Failed: &pb.FailedChatEvent{Reason: "channel is read-only"},
	}})

	// The backend's reason reaches the plugin as is.
	assertStatus(t, waitFor(t, result, "result"), codes.Unknown, "channel is read-only")
	// Plugins hear about attempts, not only successes.
	env.events.next(t)
}

func TestSendRPCsRejectUnexpectedReplies(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	rpc := sendRPCs[0]

	// A join answers join requests, not sends.
	result := callAsync(func() error { return rpc.call(env.ctx, env.plugin, rpc.target, "hi", nil, nil) })
	request, err := env.backend.recv()
	assert.NoError(t, err)
	env.backend.reply(request, joinEvent("#x", "", ""))

	assertStatus(t, waitFor(t, result, "result"), codes.Internal, "backend sent an unexpected reply")
}

func TestSendRPCsRejectBadIDs(t *testing.T) {
	t.Parallel()

	for _, rpc := range sendRPCs {
		t.Run(rpc.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)

			err := rpc.call(env.ctx, env.plugin, "irc://libera", "hi", nil, nil)

			assertStatus(t, err, codes.InvalidArgument, "invalid "+rpc.idField+`: missing "/" before the path`)
			env.assertNothingPublished(t)
		})
	}
}

func TestSendRPCsRejectBadBlocks(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	rpc := sendRPCs[0]

	err := rpc.call(env.ctx, env.plugin, rpc.target, "hi", &pb.Block{}, nil)

	assertStatus(t, err, codes.InvalidArgument, "unknown block type")
	env.assertNothingPublished(t)
}

func TestSendRPCsUnknownBackend(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	rpc := sendRPCs[0]

	err := rpc.call(env.ctx, env.plugin, "irc://nowhere/x", "hi", nil, nil)

	assertStatus(t, err, codes.NotFound, "backend not found")
	// The attempt is still announced.
	_, target, _, _, ok := rpc.event(env.events.next(t))
	assert.True(t, ok)
	assert.Equal(t, "irc://nowhere/x", target)
}

func TestSendRPCsAreAttributedToTheCaller(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	env.insertToken(t, "other-plugin", "other-token")
	rpc := sendRPCs[0]

	ctx := pluginContext(t, "other-token")
	result := callAsync(func() error { return rpc.call(ctx, env.plugin, rpc.target, "hi", nil, nil) })
	request, err := env.backend.recv()
	assert.NoError(t, err)
	env.backend.reply(request, successEvent(""))
	assert.NoError(t, waitFor(t, result, "result"))

	sender, _, _, _, ok := rpc.event(env.events.next(t))
	assert.True(t, ok)
	assert.Equal(t, "other-plugin", sender)
}
