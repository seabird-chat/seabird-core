use itertools::Itertools;
use tonic::Status;

use crate::error::RpcResult;
use crate::proto::{Block, BlockInner, TextBlock};

pub fn normalize_block(text: String, block: Option<Block>) -> RpcResult<(String, Block)> {
    // There should never be a case where a new client submits no blocks, so if
    // that's the case, this is probably from a client using the non-block-based
    // APIs and we need to add a Text block to normalize it.
    let mut block = block.unwrap_or_else(|| Block {
        plain: text.clone(),
        inner: Some(BlockInner::Text(TextBlock { text: text.clone() })),
    });

    normalize_block_inner(&mut block)?;

    let text: String = block.plain.clone();
    Ok((text, block))
}

fn normalize_block_inner(block: &mut Block) -> RpcResult<()> {
    let inner = block
        .inner
        .as_mut()
        .ok_or_else(|| Status::invalid_argument("unknown block type"))?;

    match inner {
        // Simple blocks
        BlockInner::Text(_text_block) => {}
        BlockInner::InlineCode(_inline_code_block) => {}
        BlockInner::FencedCode(_fenced_code_block) => {}
        BlockInner::Timestamp(_timestamp_block) => {}

        // Formatting blocks
        BlockInner::Italics(italics_block) => {
            let mut inner_block = italics_block
                .inner
                .as_mut()
                .ok_or_else(|| Status::invalid_argument("italics block missing inner block"))?;

            normalize_block_inner(&mut inner_block)?;
        }
        BlockInner::Bold(bold_block) => {
            let mut inner_block = bold_block
                .inner
                .as_mut()
                .ok_or_else(|| Status::invalid_argument("bold block missing inner block"))?;

            normalize_block_inner(&mut inner_block)?;
        }
        BlockInner::Underline(underline_block) => {
            let mut inner_block = underline_block
                .inner
                .as_mut()
                .ok_or_else(|| Status::invalid_argument("underline block missing inner block"))?;

            normalize_block_inner(&mut inner_block)?;
        }
        BlockInner::Strikethrough(strikethrough_block) => {
            let mut inner_block = strikethrough_block.inner.as_mut().ok_or_else(|| {
                Status::invalid_argument("strikethrough block missing inner block")
            })?;

            normalize_block_inner(&mut inner_block)?;
        }
        BlockInner::Spoiler(spoiler_block) => {
            let mut inner_block = spoiler_block
                .inner
                .as_mut()
                .ok_or_else(|| Status::invalid_argument("spoiler block missing inner block"))?;

            normalize_block_inner(&mut inner_block)?;
        }
        BlockInner::List(list_block) => {
            list_block
                .inner
                .iter_mut()
                .try_for_each(normalize_block_inner)?;
        }
        BlockInner::Link(link_block) => {
            let mut inner_block = link_block
                .inner
                .as_mut()
                .ok_or_else(|| Status::invalid_argument("link block missing inner block"))?;

            normalize_block_inner(&mut inner_block)?;
        }
        BlockInner::Blockquote(blockquote_block) => {
            let mut inner_block = blockquote_block
                .inner
                .as_mut()
                .ok_or_else(|| Status::invalid_argument("blockquote block missing inner block"))?;

            normalize_block_inner(&mut inner_block)?;
        }
        BlockInner::Container(container_block) => {
            container_block
                .inner
                .iter_mut()
                .try_for_each(normalize_block_inner)?;
        }
        BlockInner::Heading(heading_block) => {
            let mut inner_block = heading_block
                .inner
                .as_mut()
                .ok_or_else(|| Status::invalid_argument("heading block missing inner block"))?;

            normalize_block_inner(&mut inner_block)?;
        }
    };

    block.plain = render_inner_block(&inner)?;

    Ok(())
}

fn render_inner_block(block_inner: &BlockInner) -> RpcResult<String> {
    match block_inner {
        // Simple blocks
        BlockInner::Text(text_block) => Ok(text_block.text.clone()),
        BlockInner::InlineCode(inline_code_block) => Ok(inline_code_block.text.clone()),
        BlockInner::FencedCode(fenced_code_block) => Ok(fenced_code_block.text.clone()),
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
        BlockInner::Heading(heading_block) => Ok(heading_block
            .inner
            .iter()
            .map(|block| block.plain.as_str())
            .collect()),
    }
}
