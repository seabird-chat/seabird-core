package core

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
)

// sendBlock sends block to testChannelPath as the test plugin, and returns the
// request the backend got for it.
func (e *testEnv) sendBlock(t *testing.T, text string, block *pb.Block) *pb.SendMessageChatRequest {
	t.Helper()

	result := callAsync(func() error {
		_, err := e.plugin.SendMessage(e.ctx, &pb.SendMessageRequest{ChannelId: e.channelID, Text: text, RootBlock: block})
		return err
	})
	request, err := e.backend.recv()
	assert.NoError(t, err)
	e.backend.reply(request, successEvent(""))
	assert.NoError(t, waitFor(t, result, "result"))
	return request.GetSendMessage()
}

// Backends and plugins without block support read the plain text, so its
// format is part of the protocol.
func TestBlocksPlainText(t *testing.T) {
	text := newTextBlock

	tests := []struct {
		name  string
		block *pb.Block
		want  string
	}{
		{name: "text", block: text("héllo *not bold*"), want: "héllo *not bold*"},
		{name: "inline code", block: newInlineCodeBlock("x := 1"), want: "x := 1"},
		{name: "fenced code drops the info string", block: newFencedCodeBlock("go", "f()"), want: "f()"},
		{name: "timestamp in whole seconds", block: newTimestampBlock(time.Unix(5, 999)), want: "5"},
		{
			name:  "missing timestamp renders as zero",
			block: &pb.Block{Inner: &pb.Block_Timestamp{Timestamp: &pb.TimestampBlock{}}},
			want:  "0",
		},
		{name: "formatting passes its children through", block: newBoldBlock(text("a"), text("b")), want: "ab"},
		{name: "heading drops the level", block: newHeadingBlock(2, text("Title")), want: "Title"},
		{
			name:  "link appends its URL",
			block: newLinkBlock("https://example.com", text("example")),
			want:  "example (https://example.com)",
		},
		{name: "list separates items", block: newListBlock(text("one"), text("two")), want: "one, two"},
		{name: "container doesn't separate", block: newContainerBlock(text("a"), text("b")), want: "ab"},
		{
			name: "mixed message",
			block: newContainerBlock(
				newBoldBlock(text("Build")),
				text(" "),
				newLinkBlock("https://ci.example/1", text("#1")),
				text(": "),
				newListBlock(text("a"), newItalicsBlock(text("b"))),
			),
			want: "Build #1 (https://ci.example/1): a, b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)
			request := env.sendBlock(t, "ignored", tt.block)

			assert.Equal(t, tt.want, request.GetText())
			assert.Equal(t, tt.want, request.GetRootBlock().GetPlain())
		})
	}
}

func TestBlocksFromText(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	request := env.sendBlock(t, "multi\nline", nil)

	assert.Equal(t, "multi\nline", request.GetText())
	assertProtoEqual(t, &pb.Block{Plain: "multi\nline", Inner: &pb.Block_Text{Text: &pb.TextBlock{Text: "multi\nline"}}},
		request.GetRootBlock())
}

func TestBlocksReplaceSenderPlain(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	leaf := newTextBlock("real")
	leaf.Plain = "leaf lie"
	root := newBoldBlock(leaf, newItalicsBlock(newTextBlock("!")))
	root.Plain = "root lie"

	got := env.sendBlock(t, "", root).GetRootBlock()

	// Every level is filled in, not just the root.
	children := got.GetBold().GetInner().GetContainer().GetInner()
	assert.Equal(t, "real!", got.Plain)
	assert.Equal(t, "real", children[0].Plain)
	assert.Equal(t, "!", children[1].Plain)
	assert.Equal(t, "!", children[1].GetItalics().GetInner().Plain)
}

func TestBlocksRejectInvalid(t *testing.T) {
	tests := []struct {
		name    string
		block   *pb.Block
		message string
	}{
		// A present but empty block means the sender used blocks, so the text
		// isn't used to fill it in.
		{name: "block without a type", block: &pb.Block{Plain: "hello"}, message: "unknown block type"},
		{
			name:    "formatting without a child",
			block:   &pb.Block{Inner: &pb.Block_Italics{Italics: &pb.ItalicsBlock{}}},
			message: "italics block missing inner block",
		},
		{
			name: "error deep in the tree",
			block: newContainerBlock(
				newTextBlock("a"),
				newListBlock(newBoldBlock(
					&pb.Block{Inner: &pb.Block_Link{Link: &pb.LinkBlock{Url: "https://example.com"}}},
				)),
			),
			message: "link block missing inner block",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := startTestEnv(t)
			_, err := env.plugin.SendMessage(env.ctx, &pb.SendMessageRequest{
				ChannelId: env.channelID, Text: "hello", RootBlock: tt.block,
			})
			assertStatus(t, err, codes.InvalidArgument, tt.message)
			env.assertNothingPublished(t)
		})
	}
}
