package core

import (
	"context"
	"fmt"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
)

// sendRPC describes one of the four RPCs that send chat on a plugin's behalf.
type sendRPC struct {
	name    string
	target  func(t *testing.T, e *testEnv) string // an ID the plugin can send to
	path    string                                // what the backend should be told
	idField string                                // the request field holding the ID
	kind    string                                // what the ID refers to
	call    func(ctx context.Context, c pb.SeabirdClient, target, text string, block *pb.Block, tags map[string]string) error

	// request and event pull the common fields out of what the backend and the
	// plugins receive. They report false for the wrong kind of message.
	request func(*pb.ChatRequest) (path, text string, block *pb.Block, tags map[string]string, ok bool)
	event   func(*pb.Event) (sender, target, text string, block *pb.Block, ok bool)
}

// aliceSmith records a user on e's backend and returns their ID.
func aliceSmith(t *testing.T, e *testEnv) string {
	t.Helper()

	user, err := e.server.DB.RecordUser(context.Background(), e.backendID, "alice smith", "Alice Smith")
	assert.NoError(t, err)
	return user.ID
}

var sendRPCs = []sendRPC{
	{
		name:    "SendMessage",
		target:  func(t *testing.T, e *testEnv) string { return e.channelID },
		path:    testChannelPath,
		idField: "channel_id",
		kind:    "channel",
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
		target:  aliceSmith,
		path:    "alice smith",
		idField: "user_id",
		kind:    "user",
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
		target:  func(t *testing.T, e *testEnv) string { return e.channelID },
		path:    testChannelPath,
		idField: "channel_id",
		kind:    "channel",
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
		target:  aliceSmith,
		path:    "alice smith",
		idField: "user_id",
		kind:    "user",
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
			block:    func() *pb.Block { return newBoldBlock(newTextBlock("hi")) },
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
				target := rpc.target(t, env)
				result := callAsync(func() error {
					return rpc.call(env.ctx, env.plugin, target, variant.text, block, tags)
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
				sender, eventTarget, eventText, eventBlock, ok := rpc.event(event)
				assert.True(t, ok, "wrong kind of event")
				assert.Equal(t, testUsername, sender)
				assert.Equal(t, target, eventTarget)
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

	target := rpc.target(t, env)
	result := callAsync(func() error { return rpc.call(env.ctx, env.plugin, target, "hi", nil, nil) })
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
	target := rpc.target(t, env)
	result := callAsync(func() error { return rpc.call(env.ctx, env.plugin, target, "hi", nil, nil) })
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

			err := rpc.call(env.ctx, env.plugin, env.backendID, "hi", nil, nil)

			assertStatus(t, err, codes.InvalidArgument,
				fmt.Sprintf("invalid %s: %q isn't a %s ID", rpc.idField, env.backendID, rpc.kind))
			env.assertNothingPublished(t)
		})
	}
}

func TestSendRPCsRejectBadBlocks(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	rpc := sendRPCs[0]

	err := rpc.call(env.ctx, env.plugin, rpc.target(t, env), "hi", &pb.Block{}, nil)

	assertStatus(t, err, codes.InvalidArgument, "unknown block type")
	env.assertNothingPublished(t)
}

func TestSendRPCsUnreachableTarget(t *testing.T) {
	t.Parallel()

	for _, rpc := range sendRPCs {
		t.Run(rpc.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)
			unknown := idPrefixes[rpc.kind] + "_nowhere"
			known := rpc.target(t, env)

			err := rpc.call(env.ctx, env.plugin, unknown, "hi", nil, nil)
			assertStatus(t, err, codes.NotFound, rpc.kind+" not found")

			env.backend.cancel()
			env.waitForBackend(t, testBackendKey, false)
			err = rpc.call(env.ctx, env.plugin, known, "hi", nil, nil)
			assertStatus(t, err, codes.Unavailable, "backend not connected")

			// The attempts are still announced.
			for _, want := range []string{unknown, known} {
				_, target, _, _, ok := rpc.event(env.events.next(t))
				assert.True(t, ok)
				assert.Equal(t, want, target)
			}
		})
	}
}

func TestSendRPCsAreAttributedToTheCaller(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	env.insertToken(t, "other-plugin", "other-token")
	rpc := sendRPCs[0]

	ctx := pluginContext(t, "other-token")
	target := rpc.target(t, env)
	result := callAsync(func() error { return rpc.call(ctx, env.plugin, target, "hi", nil, nil) })
	request, err := env.backend.recv()
	assert.NoError(t, err)
	env.backend.reply(request, successEvent(""))
	assert.NoError(t, waitFor(t, result, "result"))

	sender, _, _, _, ok := rpc.event(env.events.next(t))
	assert.True(t, ok)
	assert.Equal(t, "other-plugin", sender)
}
