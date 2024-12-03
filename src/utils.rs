use crate::error::RpcResult;
use crate::proto::{Block, BlockInner, TextBlock};

pub fn normalize_blocks(text: String, mut blocks: Vec<Block>) -> RpcResult<(String, Vec<Block>)> {
    // There should never be a case where there are no blocks, so if that's the
    // case, this is probably from a client using the non-block-based APIs and
    // we need to add a Text block to normalize it.
    if blocks.is_empty() {
        blocks.push(Block {
            plain: text.clone(),
            inner: Some(BlockInner::Text(TextBlock { text: text.clone() })),
        });

        return Ok((text, blocks));
    }

    // If the user passed blocks, we need to normalize them so clients can use
    // the text portion if they need to.
    blocks = blocks
        .into_iter()
        .map(normalize_block)
        .collect::<RpcResult<_>>()?;

    let mut text = String::new();

    for block in blocks.iter() {
        text.push_str(&block.plain);
    }

    Ok((text, blocks))
}

pub fn normalize_block(block: Block) -> RpcResult<Block> {
    let (plain, inner) = match block.inner {
        // Simple blocks
        Some(BlockInner::Text(text_block)) => {
            (text_block.text.clone(), Some(BlockInner::Text(text_block)))
        }
        Some(BlockInner::InlineCode(inline_code_block)) => (
            format!("`{}`", inline_code_block.text),
            Some(BlockInner::InlineCode(inline_code_block)),
        ),
        Some(BlockInner::FencedCode(fenced_code_block)) => (
            format!(
                "```{}\n{}\n```\n",
                fenced_code_block.info, fenced_code_block.text
            ),
            Some(BlockInner::FencedCode(fenced_code_block)),
        ),
        Some(BlockInner::Mention(mention_block)) => (
            format!(
                "@{}",
                mention_block
                    .user
                    .as_ref()
                    .map_or_else(|| "unknown", |u| &u.id)
            ),
            Some(BlockInner::Mention(mention_block)),
        ),
        Some(BlockInner::Timestamp(timestamp_block)) => (
            format!(
                "{}",
                timestamp_block
                    .inner
                    .as_ref()
                    .map_or_else(|| 0, |t| t.seconds)
            ),
            Some(BlockInner::Timestamp(timestamp_block)),
        ),

        // Formatting blocks
        Some(BlockInner::Italics(italics_block)) => {
            let blocks = italics_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            let mut text = String::new();

            text.push_str("*");

            for block in blocks.iter() {
                text.push_str(&block.plain);
            }

            text.push_str("*");

            (
                text,
                Some(BlockInner::Italics(crate::proto::ItalicsBlock {
                    inner: blocks,
                })),
            )
        }
        Some(BlockInner::Bold(bold_block)) => {
            let blocks = bold_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            let mut text = String::new();

            text.push_str("**");

            for block in blocks.iter() {
                text.push_str(&block.plain);
            }

            text.push_str("**");

            (
                text,
                Some(BlockInner::Bold(crate::proto::BoldBlock { inner: blocks })),
            )
        }
        Some(BlockInner::Underline(underline_block)) => {
            let blocks = underline_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            let mut text = String::new();

            text.push_str("__");

            for block in blocks.iter() {
                text.push_str(&block.plain);
            }

            text.push_str("__");

            (
                text,
                Some(BlockInner::Underline(crate::proto::UnderlineBlock {
                    inner: blocks,
                })),
            )
        }
        Some(BlockInner::Strikethrough(strikethrough_block)) => {
            let blocks = strikethrough_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            let mut text = String::new();

            text.push_str("~~");

            for block in blocks.iter() {
                text.push_str(&block.plain);
            }

            text.push_str("~~");

            (
                text,
                Some(BlockInner::Strikethrough(
                    crate::proto::StrikethroughBlock { inner: blocks },
                )),
            )
        }
        Some(BlockInner::Spoiler(spoiler_block)) => {
            let blocks = spoiler_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            let mut text = String::new();

            text.push_str("||");

            for block in blocks.iter() {
                text.push_str(&block.plain);
            }

            text.push_str("||");

            (
                text,
                Some(BlockInner::Spoiler(crate::proto::SpoilerBlock {
                    inner: blocks,
                })),
            )
        }
        Some(BlockInner::List(list_block)) => {
            let blocks = list_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            let mut text = String::new();

            for block in blocks.iter() {
                text.push_str("- ");
                text.push_str(&block.plain);
                text.push('\n');
            }

            (
                text,
                Some(BlockInner::List(crate::proto::ListBlock { inner: blocks })),
            )
        }
        Some(BlockInner::Link(link_block)) => {
            let blocks = link_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            let mut text = String::new();

            text.push('[');

            for block in blocks.iter() {
                text.push_str(&block.plain);
            }

            text.push_str("](");
            text.push_str(&link_block.url);
            text.push(')');

            (
                text,
                Some(BlockInner::Link(crate::proto::LinkBlock {
                    url: link_block.url,
                    inner: blocks,
                })),
            )
        }
        Some(BlockInner::Blockquote(blockquote_block)) => {
            let blocks = blockquote_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            let mut text = String::new();

            for block in blocks.iter() {
                text.push_str(">");
                text.push_str(&block.plain);
                text.push('\n');
            }

            (
                text,
                Some(BlockInner::Blockquote(crate::proto::BlockquoteBlock {
                    inner: blocks,
                })),
            )
        }
        Some(BlockInner::Container(container_block)) => {
            let blocks = container_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            let mut text = String::new();

            for block in blocks.iter() {
                text.push_str(&block.plain);
            }

            (
                text,
                Some(BlockInner::Blockquote(crate::proto::BlockquoteBlock {
                    inner: blocks,
                })),
            )
        }
        None => (block.plain, None),
    };

    Ok(Block { plain, inner })
}
