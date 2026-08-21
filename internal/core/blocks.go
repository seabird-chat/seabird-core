package core

import (
	"strconv"
	"strings"

	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file is the only implementation of the block flattening rules. The proto
// promises that "Seabird will always re-hydrate the plain property from the
// blocks", so clients leave Block.Plain empty and everything below recomputes
// it. Don't reintroduce a second copy in a client library: the two drifted for a
// long time before this became the single source of truth, and because core
// overwrites whatever a client sends, nobody noticed.

// originalFormatTag records whether the sender used the block API or the older
// text-only API, so backends can tell a synthesized block tree from a real one.
const originalFormatTag = "core/original-format"

// normalizeBlock recomputes the Plain field of every block in the tree and
// returns the flattened text along with the tags describing the original
// format. A nil block means the sender used the text-only API, so a Text block
// is synthesized to normalize it.
func normalizeBlock(text string, block *pb.Block) (string, *pb.Block, map[string]string, error) {
	originalFormat := "blocks"

	if block == nil {
		originalFormat = "text"
		block = &pb.Block{
			Plain: text,
			Inner: &pb.Block_Text{Text: &pb.TextBlock{Text: text}},
		}
	}

	if err := normalizeBlockInner(block); err != nil {
		return "", nil, nil, err
	}

	return block.Plain, block, map[string]string{originalFormatTag: originalFormat}, nil
}

// normalizeBlockInner normalizes children depth-first, then renders this
// block's Plain from the already-normalized children.
func normalizeBlockInner(block *pb.Block) error {
	if block.Inner == nil {
		return status.Error(codes.InvalidArgument, "unknown block type")
	}

	var err error

	switch inner := block.Inner.(type) {
	// Simple blocks carry their own text and have no children.
	case *pb.Block_Text, *pb.Block_InlineCode, *pb.Block_FencedCode, *pb.Block_Timestamp:

	// Formatting blocks wrap exactly one child.
	case *pb.Block_Italics:
		err = normalizeChild("italics", inner.Italics.GetInner())
	case *pb.Block_Bold:
		err = normalizeChild("bold", inner.Bold.GetInner())
	case *pb.Block_Underline:
		err = normalizeChild("underline", inner.Underline.GetInner())
	case *pb.Block_Strikethrough:
		err = normalizeChild("strikethrough", inner.Strikethrough.GetInner())
	case *pb.Block_Spoiler:
		err = normalizeChild("spoiler", inner.Spoiler.GetInner())
	case *pb.Block_Link:
		err = normalizeChild("link", inner.Link.GetInner())
	case *pb.Block_Blockquote:
		err = normalizeChild("blockquote", inner.Blockquote.GetInner())
	case *pb.Block_Heading:
		err = normalizeChild("heading", inner.Heading.GetInner())

	// Container blocks wrap any number of children.
	case *pb.Block_List:
		err = normalizeChildren(inner.List.GetInner())
	case *pb.Block_Container:
		err = normalizeChildren(inner.Container.GetInner())

	default:
		return status.Error(codes.InvalidArgument, "unknown block type")
	}

	if err != nil {
		return err
	}

	block.Plain = renderBlockPlain(block)

	return nil
}

func normalizeChild(name string, child *pb.Block) error {
	if child == nil {
		return status.Errorf(codes.InvalidArgument, "%s block missing inner block", name)
	}

	return normalizeBlockInner(child)
}

func normalizeChildren(children []*pb.Block) error {
	for _, child := range children {
		if err := normalizeBlockInner(child); err != nil {
			return err
		}
	}

	return nil
}

// renderBlockPlain builds the plain text for a block whose children have
// already been normalized.
func renderBlockPlain(block *pb.Block) string {
	switch inner := block.Inner.(type) {
	case *pb.Block_Text:
		return inner.Text.GetText()
	case *pb.Block_InlineCode:
		return inner.InlineCode.GetText()
	case *pb.Block_FencedCode:
		return inner.FencedCode.GetText()
	case *pb.Block_Timestamp:
		// TODO: this should error on a missing timestamp.
		return strconv.FormatInt(inner.Timestamp.GetInner().GetSeconds(), 10)

	case *pb.Block_Italics:
		return inner.Italics.GetInner().GetPlain()
	case *pb.Block_Bold:
		return inner.Bold.GetInner().GetPlain()
	case *pb.Block_Underline:
		return inner.Underline.GetInner().GetPlain()
	case *pb.Block_Strikethrough:
		return inner.Strikethrough.GetInner().GetPlain()
	case *pb.Block_Spoiler:
		return inner.Spoiler.GetInner().GetPlain()
	case *pb.Block_Blockquote:
		return inner.Blockquote.GetInner().GetPlain()
	case *pb.Block_Heading:
		return inner.Heading.GetInner().GetPlain()
	case *pb.Block_Link:
		return renderLink(inner.Link)

	case *pb.Block_List:
		return joinBlocks(inner.List.GetInner(), ", ")
	case *pb.Block_Container:
		return joinBlocks(inner.Container.GetInner(), "")

	default:
		return ""
	}
}

// renderLink flattens a link to "text (url)", or to just the URL when the two
// would duplicate each other. Linkifiers wrap bare URLs in a link whose text is
// the URL itself, and "https://x (https://x)" is noise on backends which only
// read the flattened text.
func renderLink(link *pb.LinkBlock) string {
	text := link.GetInner().GetPlain()
	url := link.GetUrl()

	if text == "" || sameTarget(text, url) {
		return url
	}

	return text + " (" + url + ")"
}

// sameTarget reports whether the link text is just the URL written differently.
// Linkifiers add a scheme to bare hosts and addresses, so "www.x.com" against
// "http://www.x.com" counts as a duplicate too.
func sameTarget(text, url string) bool {
	if text == url {
		return true
	}

	for _, scheme := range []string{"https://", "http://", "mailto:"} {
		if strings.TrimPrefix(url, scheme) == text {
			return true
		}
	}

	return false
}

func joinBlocks(blocks []*pb.Block, sep string) string {
	plain := make([]string, 0, len(blocks))
	for _, block := range blocks {
		plain = append(plain, block.GetPlain())
	}

	return strings.Join(plain, sep)
}
