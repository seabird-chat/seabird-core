#[macro_use]
extern crate log;

use std::collections::BTreeMap;

use tokio::fs::File;
use tokio::io::AsyncReadExt;
use tokio::signal::unix::{signal, SignalKind};

mod error;
mod id;
mod prelude;
mod server;

use crate::prelude::*;

pub mod proto {
    pub mod common {
        tonic::include_proto!("common");
    }

    pub mod seabird {
        tonic::include_proto!("seabird");
    }

    pub use self::common::*;
    pub use self::seabird::*;

    // The nested types are fairly annoying to reference, so we make some
    // convenience aliases.
    pub use self::chat_event::Inner as ChatEventInner;
    pub use self::chat_request::Inner as ChatRequestInner;
    pub use self::event::Inner as EventInner;
}

pub fn spawn<T, V>(mut sender: tokio::sync::mpsc::Sender<T::Output>, task: T)
where
    T: futures::Future<Output = RpcResult<V>> + Send + 'static,
    T::Output: Send + 'static,
    V: Send + 'static,
{
    tokio::spawn(async move {
        let res = task.await;

        if let Err(err) = res {
            error!("error when running stream: {}", err);

            let _ = sender.send(Err(err)).await;
        }
    });
}

#[derive(serde::Deserialize)]
struct Tokens {
    tokens: BTreeMap<String, String>,
}

async fn read_tokens(filename: &str) -> Result<BTreeMap<String, String>> {
    let mut buf = String::new();
    let mut file = File::open(filename).await?;

    file.read_to_string(&mut buf).await?;

    let tokens: Tokens = serde_json::from_str(&buf)?;

    // In the config file we use tag -> token because that makes the most
    // sense, but we need to reverse it before passing it in to the server.
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

    match env_res {
        Ok(path) => info!("Loaded env from {:?}", path),

        // If the .env file doesn't exist, that's fine. All other errors are
        // actually an issue.
        Err(dotenv::Error::Io(io_err)) if io_err.kind() == std::io::ErrorKind::NotFound => {}
        Err(_) => {
            env_res?;
        }
    }

    let server = crate::server::Server::new(
        std::env::var("SEABIRD_BIND_HOST").unwrap_or_else(|_| "0.0.0.0:11235".to_string()),
    )?;

    let token_file = dotenv::var("SEABIRD_TOKEN_FILE")
        .context("Missing $SEABIRD_TOKEN_FILE. You must specify a token file for the bot.")?;

    // Read in the tokens so the server can have them set without needing a
    // SIGHUP.
    let tokens = read_tokens(&token_file).await?;
    server.set_tokens(tokens).await;

    // Spawn our token reader task
    let mut signal_stream = signal(SignalKind::hangup())?;
    let tokens_server = server.clone();
    tokio::spawn(async move {
        loop {
            signal_stream.recv().await;

            info!("got SIGHUP, reloading tokens");

            match read_tokens(&token_file).await {
                Ok(tokens) => {
                    tokens_server.set_tokens(tokens).await;
                    info!("reloaded tokens");
                },
                Err(err) => warn!("failed to reload tokens: {}", err)
            }
        }
    });

    // Wait on the server
    server.run().await
}
