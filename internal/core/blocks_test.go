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

// Linkifiers wrap bare URLs in a link whose text is the URL, so flattening the
// naive way duplicates it. Anything a user actually wrote link text for still
// gets both halves.
func TestNormalizeBlockCollapsesRedundantLinks(t *testing.T) {
	tests := []struct {
		name string
		url  string
		text string
		want string
	}{
		{"autolinked url", "https://seabird.chat", "https://seabird.chat", "https://seabird.chat"},
		{"http autolink", "http://seabird.chat", "http://seabird.chat", "http://seabird.chat"},
		{"scheme added to bare host", "http://www.example.com", "www.example.com", "http://www.example.com"},
		{"scheme added to address", "mailto:a@b.com", "a@b.com", "mailto:a@b.com"},
		{"empty link text", "https://example.com", "", "https://example.com"},

		{
			"real link text is kept",
			"https://example.com", "example",
			"example (https://example.com)",
		},
		{
			"different host is kept",
			"https://example.com", "evil.com",
			"evil.com (https://example.com)",
		},
		{
			// Trailing slashes aren't normalized, so this still duplicates.
			// Documented rather than fixed: URL normalization doesn't belong here.
			"trailing slash still duplicates",
			"https://example.com/", "https://example.com",
			"https://example.com (https://example.com/)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, _, _, err := normalizeBlock("", &pb.Block{
				Inner: &pb.Block_Link{Link: &pb.LinkBlock{
					Url:   test.url,
					Inner: textBlock(test.text),
				}},
			})
			assert.NoError(t, err)
			assert.Equal(t, test.want, text)
		})
	}
}

// The case which started this: a bare URL pasted in Discord arrives as text
// plus an autolinked LinkBlock, and backends which only read the flattened text
// used to see the URL twice.
func TestNormalizeBlockLinkifiedMessage(t *testing.T) {
	text, _, _, err := normalizeBlock("", &pb.Block{
		Inner: &pb.Block_Container{Container: &pb.ContainerBlock{Inner: []*pb.Block{
			textBlock("hello "),
			{Inner: &pb.Block_Link{Link: &pb.LinkBlock{
				Url:   "https://seabird.chat",
				Inner: textBlock("https://seabird.chat"),
			}}},
		}}},
	})
	assert.NoError(t, err)
	assert.Equal(t, "hello https://seabird.chat", text)
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
