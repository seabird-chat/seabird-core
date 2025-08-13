// Some Itertools functions collide with proposed stdlib functions, but the lint
// isn't very helpful, so we disable it for now.
#![allow(unstable_name_collisions)]

use std::io::{IsTerminal, stdout};

mod db;
mod id;
mod prelude;
pub mod proto;
mod server;
mod utils;
mod wrapped;

use crate::prelude::*;

pub mod error {
    pub use anyhow::{Error, Result};
    pub type RpcResult<T> = std::result::Result<T, tonic::Status>;
}

pub fn spawn<T, V>(task: T)
where
    T: futures::Future<Output = RpcResult<V>> + Send + 'static,
    T::Output: Send + 'static,
    V: Send + 'static,
{
    tokio::spawn(async move {
        if let Err(err) = task.await {
            error!("error when running stream: {}", err);
        }
    });
}

#[tokio::main]
async fn main() -> Result<()> {
    // Try to load dotenv before loading the logger or trying to set defaults.
    let env_res = dotenvy::dotenv();

    // There's a little bit of an oddity here, since we want to set it if it
    // hasn't already been set, but we want this done before the logger is loaded.
    if std::env::var("RUST_LOG").is_err() {
        std::env::set_var("RUST_LOG", "info,seabird::server=trace");
    }

    // Now that everything is set up, load up the logger. We assume that if
    // there is no tty on stdout, it's in production mode, and thus we configure
    // it in json mode, otherwise, we make it look better for development.
    if !stdout().is_terminal() {
        tracing_subscriber::fmt().json().init();
    } else {
        tracing_subscriber::fmt()
            .compact()
            .with_file(true)
            .with_line_number(true)
            .with_thread_ids(true)
            .with_target(false)
            .init();
    }

    match env_res {
        Ok(path) => info!("Loaded env from {:?}", path),

        // If the .env file doesn't exist, that's fine. All other errors are
        // actually an issue.
        Err(dotenvy::Error::Io(io_err)) if io_err.kind() == std::io::ErrorKind::NotFound => {}
        Err(_) => {
            env_res?;
        }
    }

    let server = crate::server::Server::new(
        std::env::var("SEABIRD_BIND_HOST").unwrap_or_else(|_| "0.0.0.0:11235".to_string()),
        std::env::var("DATABASE_URL").unwrap(),
    )?;

    // Wait on the server
    server.run().await
}
