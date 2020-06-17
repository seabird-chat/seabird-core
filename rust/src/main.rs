#[macro_use]
extern crate log;

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

fn main() -> Result<()> {
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

    // Because our entrypoint is a single task we can get away with not using
    // tokio's macros by constructing a runtime directly.
    let mut runtime = tokio::runtime::Runtime::new()?;
    runtime.block_on(server.run())
}
