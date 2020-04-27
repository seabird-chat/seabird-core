#![warn(missing_debug_implementations, rust_2018_idioms)]

#[macro_use]
extern crate log;

mod error;
mod irc;
mod prelude;
mod server;

pub mod proto {
    tonic::include_proto!("seabird");
}

use prelude::*;
use server::{Server, ServerConfig};

#[tokio::main]
async fn main() -> Result<()> {
    // Try to load dotenv before loading the logger or trying to set defaults.
    let env_res = dotenv::dotenv();

    // There's a little bit of an oddity here, since we want to set it if it
    // hasn't already been set, but we want this done before the logger is loaded.
    if std::env::var("RUST_LOG").is_err() {
        std::env::set_var("RUST_LOG", "info,seabird=trace");
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

    Server::new(config).run().await
}
