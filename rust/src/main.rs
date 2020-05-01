#![warn(missing_debug_implementations, rust_2018_idioms)]

#[macro_use]
extern crate log;

use std::path::PathBuf;

use notify::{event::AccessKind, EventKind, RecommendedWatcher, RecursiveMode, Watcher};

mod error;
mod irc;
mod prelude;
mod server;

pub mod proto {
    tonic::include_proto!("seabird");
}

use prelude::*;
use server::{Server, ServerConfig};

#[derive(serde::Deserialize)]
struct Tokens {
    // In the config file we use owner -> token because that makes the most
    // sense, but we need to reverse it before passing it in to the server.
    tokens: BTreeMap<String, String>,
}

fn read_tokens(filename: &str) -> Result<BTreeMap<String, String>> {
    let tokens: Tokens = serde_json::from_str(&std::fs::read_to_string(filename)?)?;

    // Reverse the mapping of key and value.
    Ok(tokens.tokens.into_iter().map(|(k, v)| (v, k)).collect())
}

#[tokio::main]
async fn main() -> Result<()> {
    // Try to load dotenv before loading the logger or trying to set defaults.
    let env_res = dotenv::dotenv();

    // There's a little bit of an oddity here, since we want to set it if it
    // hasn't already been set, but we want this done before the logger is loaded.
    if std::env::var("RUST_LOG").is_err() {
        std::env::set_var("RUST_LOG", "info,seabird::server=trace");
    }

    // Now that everything is set up, load up the logger.
    pretty_env_logger::init_timed();

    // We ignore failures here because we want to fall back to loading from the
    // environment.
    if let Ok(path) = env_res {
        info!("Loaded env from {:?}", path);
    }

    // Load our config from command line arguments
    let config = ServerConfig::new(
        dotenv::var("SEABIRD_BIND_HOST").unwrap_or_else(|_| "0.0.0.0:11235".to_string()),
        dotenv::var("SEABIRD_IRC_HOST").context(
            "Missing $SEABIRD_IRC_HOST. You must specify a host for the bot to connect to.",
        )?,
        dotenv::var("SEABIRD_NICK")
            .context("Missing $SEABIRD_NICK. You must specify a nickname for the bot.")?,
        dotenv::var("SEABIRD_USER").ok(),
        dotenv::var("SEABIRD_NAME").ok(),
        dotenv::var("SEABIRD_PASS").ok(),
        dotenv::var("SEABIRD_COMMAND_PREFIX").ok(),
    );

    let token_file = dotenv::var("SEABIRD_TOKEN_FILE")
        .context("Missing $SEABIRD_TOKEN_FILE. You must specify a token file for the bot.")?;

    let watched_filename = PathBuf::from(&token_file)
        .file_name()
        .unwrap()
        .to_os_string();
    let watch_dir = PathBuf::from(&token_file)
        .parent()
        .unwrap()
        .as_os_str()
        .to_os_string();

    let server = Server::new(config);
    let sender = server.get_internal_sender();

    sender.send(server::InternalEvent::SetTokens {
        tokens: read_tokens(&token_file)?,
    })?;

    let mut watcher: RecommendedWatcher =
        Watcher::new_immediate(move |res: notify::Result<notify::Event>| match res {
            Ok(event) => match event.kind {
                EventKind::Access(AccessKind::Close(_)) => {
                    if event
                        .paths
                        .iter()
                        .any(|path| path.ends_with(&watched_filename))
                    {
                        match read_tokens(&token_file) {
                            Err(err) => error!("failed to read tokens: {}", err),
                            Ok(tokens) => sender
                                .send(server::InternalEvent::SetTokens { tokens })
                                .unwrap(),
                        }
                    }
                }
                _ => {}
            },
            Err(e) => println!("watch error: {:?}", e),
        })?;

    // Add a path to be watched. All files and directories at that path and
    // below will be monitored for changes.
    watcher.watch(watch_dir, RecursiveMode::NonRecursive)?;

    server.run().await
}
