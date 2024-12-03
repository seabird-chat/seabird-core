use itertools::Itertools;
use tonic::Status;

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

    let text: String = blocks.iter().map(|block| block.plain.as_str()).collect();

    Ok((text, blocks))
}

fn normalize_block(block: Block) -> RpcResult<Block> {
    let inner = block
        .inner
        .ok_or_else(|| Status::invalid_argument("unknown block type"))?;

    let inner = match inner {
        // Simple blocks
        BlockInner::Text(text_block) => BlockInner::Text(text_block),
        BlockInner::InlineCode(inline_code_block) => BlockInner::InlineCode(inline_code_block),
        BlockInner::FencedCode(fenced_code_block) => BlockInner::FencedCode(fenced_code_block),
        BlockInner::Mention(mention_block) => BlockInner::Mention(mention_block),
        BlockInner::Timestamp(timestamp_block) => BlockInner::Timestamp(timestamp_block),

        // Formatting blocks
        BlockInner::Italics(italics_block) => {
            let blocks = italics_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            BlockInner::Italics(crate::proto::ItalicsBlock { inner: blocks })
        }
        BlockInner::Bold(bold_block) => {
            let blocks = bold_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            BlockInner::Bold(crate::proto::BoldBlock { inner: blocks })
        }
        BlockInner::Underline(underline_block) => {
            let blocks = underline_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            BlockInner::Underline(crate::proto::UnderlineBlock { inner: blocks })
        }
        BlockInner::Strikethrough(strikethrough_block) => {
            let blocks = strikethrough_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            BlockInner::Strikethrough(crate::proto::StrikethroughBlock { inner: blocks })
        }
        BlockInner::Spoiler(spoiler_block) => {
            let blocks = spoiler_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            BlockInner::Spoiler(crate::proto::SpoilerBlock { inner: blocks })
        }
        BlockInner::List(list_block) => {
            let blocks = list_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            BlockInner::List(crate::proto::ListBlock { inner: blocks })
        }
        BlockInner::Link(link_block) => {
            let blocks = link_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            BlockInner::Link(crate::proto::LinkBlock {
                url: link_block.url,
                inner: blocks,
            })
        }
        BlockInner::Blockquote(blockquote_block) => {
            let blocks = blockquote_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            BlockInner::Blockquote(crate::proto::BlockquoteBlock { inner: blocks })
        }
        BlockInner::Container(container_block) => {
            let blocks = container_block
                .inner
                .into_iter()
                .map(normalize_block)
                .collect::<RpcResult<Vec<_>>>()?;

            BlockInner::Container(crate::proto::ContainerBlock { inner: blocks })
        }
    };

    Ok(Block {
        plain: render_inner_block(&inner)?,
        inner: Some(inner),
    })
}

fn render_inner_block(block_inner: &BlockInner) -> RpcResult<String> {
    match block_inner {
        // Simple blocks
        BlockInner::Text(text_block) => Ok(text_block.text.clone()),
        BlockInner::InlineCode(inline_code_block) => Ok(inline_code_block.text.clone()),
        BlockInner::FencedCode(fenced_code_block) => Ok(fenced_code_block.text.clone()),
        BlockInner::Mention(mention_block) =>
        // TODO: this should error on missing user
        {
            Ok(format!(
                "@{}",
                mention_block
                    .user
                    .as_ref()
                    .map_or_else(|| "unknown", |u| &u.id)
            ))
        }
        BlockInner::Timestamp(timestamp_block) =>
        // TODO: this should error on missing timestamp
        {
            Ok(format!(
                "{}",
                timestamp_block
                    .inner
                    .as_ref()
                    .map_or_else(|| 0, |t| t.seconds)
            ))
        }

        // Formatting blocks
        BlockInner::Italics(italics_block) => Ok(italics_block
            .inner
            .iter()
            .map(|block| block.plain.as_str())
            .collect()),
        BlockInner::Bold(bold_block) => Ok(bold_block
            .inner
            .iter()
            .map(|block| block.plain.as_str())
            .collect()),
        BlockInner::Underline(underline_block) => Ok(underline_block
            .inner
            .iter()
            .map(|block| block.plain.as_str())
            .collect()),
        BlockInner::Strikethrough(strikethrough_block) => Ok(strikethrough_block
            .inner
            .iter()
            .map(|block| block.plain.as_str())
            .collect()),
        BlockInner::Spoiler(spoiler_block) => Ok(spoiler_block
            .inner
            .iter()
            .map(|block| block.plain.as_str())
            .collect()),
        BlockInner::List(list_block) => Ok(list_block
            .inner
            .iter()
            .map(|block| block.plain.as_str())
            .intersperse(", ")
            .collect()),
        BlockInner::Link(link_block) => {
            let mut text: String = link_block
                .inner
                .iter()
                .map(|block| block.plain.as_str())
                .collect();

            text.push_str(" (");
            text.push_str(&link_block.url);
            text.push_str(")");

            Ok(text)
        }
        BlockInner::Blockquote(blockquote_block) => Ok(blockquote_block
            .inner
            .iter()
            .map(|block| block.plain.as_str())
            .collect()),
        BlockInner::Container(container_block) => Ok(container_block
            .inner
            .iter()
            .map(|block| block.plain.as_str())
            .collect()),
    }
}
