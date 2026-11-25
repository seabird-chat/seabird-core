package core

import (
	"strconv"
	"strings"

	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// normalizeBlock wraps text-only content in a Text block (when block is nil)
// and recomputes Plain on every block in the tree, discarding any value the
// sender supplied. The block is modified in place. Errors are InvalidArgument
// statuses.
func normalizeBlock(text string, block *pb.Block) (*pb.Block, error) {
	if block == nil {
		block = &pb.Block{Inner: &pb.Block_Text{Text: &pb.TextBlock{Text: text}}}
	}

	if err := renderPlain(block); err != nil {
		return nil, err
	}
	return block, nil
}

// renderPlain renders block and its descendants as plain text, storing the
// result in each one's Plain field, children first.
func renderPlain(block *pb.Block) error {
	var plain string

	switch inner := block.GetInner().(type) {
	case *pb.Block_Text:
		plain = inner.Text.GetText()
	case *pb.Block_InlineCode:
		plain = inner.InlineCode.GetText()
	case *pb.Block_FencedCode:
		plain = inner.FencedCode.GetText()
	case *pb.Block_Timestamp:
		// A missing timestamp renders as zero rather than failing.
		plain = strconv.FormatInt(inner.Timestamp.GetInner().GetSeconds(), 10)

	case *pb.Block_Italics:
		return renderWrapperPlain(block, "italics", inner.Italics.GetInner())
	case *pb.Block_Bold:
		return renderWrapperPlain(block, "bold", inner.Bold.GetInner())
	case *pb.Block_Underline:
		return renderWrapperPlain(block, "underline", inner.Underline.GetInner())
	case *pb.Block_Strikethrough:
		return renderWrapperPlain(block, "strikethrough", inner.Strikethrough.GetInner())
	case *pb.Block_Spoiler:
		return renderWrapperPlain(block, "spoiler", inner.Spoiler.GetInner())
	case *pb.Block_Blockquote:
		return renderWrapperPlain(block, "blockquote", inner.Blockquote.GetInner())
	case *pb.Block_Heading:
		return renderWrapperPlain(block, "heading", inner.Heading.GetInner())

	case *pb.Block_Link:
		if err := renderWrapperPlain(block, "link", inner.Link.GetInner()); err != nil {
			return err
		}
		block.Plain += " (" + inner.Link.GetUrl() + ")"
		return nil

	case *pb.Block_List:
		parts, err := renderChildrenPlain(inner.List.GetInner())
		if err != nil {
			return err
		}
		plain = strings.Join(parts, ", ")
	case *pb.Block_Container:
		parts, err := renderChildrenPlain(inner.Container.GetInner())
		if err != nil {
			return err
		}
		plain = strings.Join(parts, "")

	default:
		return status.Error(codes.InvalidArgument, "unknown block type")
	}

	block.Plain = plain
	return nil
}

// renderWrapperPlain handles blocks that format a single child: the block's
// plain text is the child's.
func renderWrapperPlain(block *pb.Block, name string, child *pb.Block) error {
	if child == nil {
		return status.Errorf(codes.InvalidArgument, "%s block missing inner block", name)
	}
	if err := renderPlain(child); err != nil {
		return err
	}

	block.Plain = child.Plain
	return nil
}

// renderChildrenPlain renders each child and returns their plain text in order.
func renderChildrenPlain(children []*pb.Block) ([]string, error) {
	parts := make([]string, 0, len(children))
	for _, child := range children {
		if err := renderPlain(child); err != nil {
			return nil, err
		}
		parts = append(parts, child.Plain)
	}
	return parts, nil
}
