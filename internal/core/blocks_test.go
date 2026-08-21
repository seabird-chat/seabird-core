package core

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func textBlock(text string) *pb.Block {
	return &pb.Block{Inner: &pb.Block_Text{Text: &pb.TextBlock{Text: text}}}
}

func TestNormalizeBlockSynthesizesTextBlock(t *testing.T) {
	text, block, tags, err := normalizeBlock("hello", nil)
	assert.NoError(t, err)
	assert.Equal(t, "hello", text)
	assert.Equal(t, "hello", block.GetPlain())
	assert.Equal(t, "hello", block.GetText().GetText())
	assert.Equal(t, map[string]string{originalFormatTag: "text"}, tags)
}

func TestNormalizeBlockTagsBlockSenders(t *testing.T) {
	// The text argument is ignored when a block tree is supplied: the
	// flattened tree is authoritative.
	text, _, tags, err := normalizeBlock("ignored", textBlock("hello"))
	assert.NoError(t, err)
	assert.Equal(t, "hello", text)
	assert.Equal(t, map[string]string{originalFormatTag: "blocks"}, tags)
}

func TestNormalizeBlockPlain(t *testing.T) {
	tests := []struct {
		name  string
		block *pb.Block
		want  string
	}{
		{
			"nested formatting",
			&pb.Block{Inner: &pb.Block_Bold{Bold: &pb.BoldBlock{
				Inner: &pb.Block{Inner: &pb.Block_Italics{Italics: &pb.ItalicsBlock{
					Inner: textBlock("hi"),
				}}},
			}}},
			"hi",
		},
		{
			"container concatenates",
			&pb.Block{Inner: &pb.Block_Container{Container: &pb.ContainerBlock{
				Inner: []*pb.Block{textBlock("a"), textBlock("b")},
			}}},
			"ab",
		},
		{
			"list is comma separated",
			&pb.Block{Inner: &pb.Block_List{List: &pb.ListBlock{
				Inner: []*pb.Block{textBlock("a"), textBlock("b")},
			}}},
			"a, b",
		},
		{
			"link appends url",
			&pb.Block{Inner: &pb.Block_Link{Link: &pb.LinkBlock{
				Url:   "https://example.com",
				Inner: textBlock("example"),
			}}},
			"example (https://example.com)",
		},
		{
			"inline code",
			&pb.Block{Inner: &pb.Block_InlineCode{InlineCode: &pb.InlineCodeBlock{Text: "x := 1"}}},
			"x := 1",
		},
		{
			"missing timestamp renders zero",
			&pb.Block{Inner: &pb.Block_Timestamp{Timestamp: &pb.TimestampBlock{}}},
			"0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Plain is deliberately wrong going in, to prove it gets
			// recomputed rather than trusted.
			test.block.Plain = "stale"

			text, block, _, err := normalizeBlock("", test.block)
			assert.NoError(t, err)
			assert.Equal(t, test.want, text)
			assert.Equal(t, test.want, block.GetPlain())
		})
	}
}

func TestNormalizeBlockErrors(t *testing.T) {
	tests := []struct {
		name  string
		block *pb.Block
	}{
		{"no inner", &pb.Block{}},
		{
			"formatting block without child",
			&pb.Block{Inner: &pb.Block_Bold{Bold: &pb.BoldBlock{}}},
		},
		{
			"nested block without child",
			&pb.Block{Inner: &pb.Block_Container{Container: &pb.ContainerBlock{
				Inner: []*pb.Block{{Inner: &pb.Block_Italics{Italics: &pb.ItalicsBlock{}}}},
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := normalizeBlock("", test.block)
			assert.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}
