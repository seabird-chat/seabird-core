use std::fmt::Write;

use crate::prelude::*;
use crate::proto::{Block, BlockInner};

pub fn normalize_message(
    raw_text: &str,
    mut blocks: Vec<Block>,
) -> RpcResult<(String, Vec<Block>)> {
    if blocks.len() == 0 {
        return Ok((
            raw_text.to_string(),
            vec![Block::new_plain(raw_text.to_string())],
        ));
    }

    let plain_buf = render_multiple_blocks(&mut blocks)?;
    Ok((plain_buf, blocks))
}

fn render_multiple_blocks(blocks: &mut Vec<Block>) -> RpcResult<String> {
    let mut plain_buf = String::new();

    for block in blocks.iter_mut() {
        render_block(block)?;
        plain_buf
            .write_str(&block.plain)
            .map_err(|_err| Status::internal("failed to render block"))?;
    }

    Ok(plain_buf)
}

fn render_block(block: &mut Block) -> RpcResult<()> {
    let inner = block
        .inner
        .as_mut()
        .ok_or_else(|| tonic::Status::invalid_argument("unknown block type"))?;

    block.plain = match inner {
        BlockInner::Text(text) => text.text.clone(),
        BlockInner::Italics(italics) => render_multiple_blocks(&mut italics.inner)?,
        BlockInner::Bold(bold) => render_multiple_blocks(&mut bold.inner)?,
        BlockInner::Underline(underline) => render_multiple_blocks(&mut underline.inner)?,
        BlockInner::Strikethrough(strikethrough) => {
            render_multiple_blocks(&mut strikethrough.inner)?
        }
        BlockInner::InlineCode(inline_code) => inline_code.text.clone(),
        BlockInner::FencedCode(fenced_code) => fenced_code.text.clone(),
        BlockInner::Mention(mention) => {
            format!(
                "@{}",
                mention
                    .user
                    .as_ref()
                    .map(|user| user.display_name.as_str())
                    .unwrap_or_else(|| "unknown")
            )
        }
        BlockInner::Spoiler(spoiler) => render_multiple_blocks(&mut spoiler.inner)?,
        BlockInner::Action(action) => render_multiple_blocks(&mut action.inner)?,
        BlockInner::List(list) => {
            let mut buf = String::new();

            for item in list.items.iter_mut() {
                render_block(item)?;
                write!(buf, "- {}", item.plain)
                    .map_err(|_err| Status::internal("failed to render block"))?;
            }

            buf
        }
        BlockInner::Link(link) => format!(
            "{} ({})",
            render_multiple_blocks(&mut link.inner)?,
            link.url
        ),
        BlockInner::Blockquote(blockquote) => render_multiple_blocks(&mut blockquote.inner)?,
    };

    Ok(())
}
