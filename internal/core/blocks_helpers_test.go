package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// These build blocks the way seabird-go's constructors do, with Plain filled
// in, so tests can describe what clients send.

func newTextBlock(text string) *pb.Block {
	return &pb.Block{Plain: text, Inner: &pb.Block_Text{Text: &pb.TextBlock{Text: text}}}
}

func newInlineCodeBlock(text string) *pb.Block {
	return &pb.Block{Plain: text, Inner: &pb.Block_InlineCode{InlineCode: &pb.InlineCodeBlock{Text: text}}}
}

func newFencedCodeBlock(info, text string) *pb.Block {
	return &pb.Block{Plain: text, Inner: &pb.Block_FencedCode{FencedCode: &pb.FencedCodeBlock{Info: info, Text: text}}}
}

func newItalicsBlock(inner ...*pb.Block) *pb.Block {
	return &pb.Block{
		Plain: plainText(inner),
		Inner: &pb.Block_Italics{Italics: &pb.ItalicsBlock{Inner: maybeContainer(inner)}},
	}
}

func newBoldBlock(inner ...*pb.Block) *pb.Block {
	return &pb.Block{
		Plain: plainText(inner),
		Inner: &pb.Block_Bold{Bold: &pb.BoldBlock{Inner: maybeContainer(inner)}},
	}
}

func newHeadingBlock(level int, inner ...*pb.Block) *pb.Block {
	return &pb.Block{
		Plain: plainText(inner),
		Inner: &pb.Block_Heading{Heading: &pb.HeadingBlock{Level: int32(level), Inner: maybeContainer(inner)}},
	}
}

func newContainerBlock(inner ...*pb.Block) *pb.Block {
	return &pb.Block{
		Plain: plainText(inner),
		Inner: &pb.Block_Container{Container: &pb.ContainerBlock{Inner: inner}},
	}
}

func newListBlock(inner ...*pb.Block) *pb.Block {
	return &pb.Block{
		Plain: plainText(inner),
		Inner: &pb.Block_List{List: &pb.ListBlock{Inner: inner}},
	}
}

func newLinkBlock(url string, inner ...*pb.Block) *pb.Block {
	return &pb.Block{
		Plain: fmt.Sprintf("%s (%s)", plainText(inner), url),
		Inner: &pb.Block_Link{Link: &pb.LinkBlock{Url: url, Inner: maybeContainer(inner)}},
	}
}

func newTimestampBlock(target time.Time) *pb.Block {
	return &pb.Block{
		Plain: target.Format(time.Stamp),
		Inner: &pb.Block_Timestamp{Timestamp: &pb.TimestampBlock{Inner: timestamppb.New(target)}},
	}
}

// plainText joins the plain text of blocks.
func plainText(blocks []*pb.Block) string {
	var b strings.Builder
	for _, block := range blocks {
		b.WriteString(block.GetPlain())
	}
	return b.String()
}

// maybeContainer wraps blocks in a container unless there's only one.
func maybeContainer(blocks []*pb.Block) *pb.Block {
	if len(blocks) == 1 {
		return blocks[0]
	}
	return newContainerBlock(blocks...)
}
