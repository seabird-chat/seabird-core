use std::collections::BTreeMap;
use std::collections::HashMap;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use tokio::sync::{broadcast, mpsc, oneshot, Mutex, RwLock};

use crate::prelude::*;

use proto::{
    seabird::chat_ingest_server::ChatIngestServer, seabird::seabird_server::SeabirdServer,
    ChatEventInner, EventInner,
};

mod auth;
mod grpc;
mod ingest_events;

// TODO: make these configurable using environment variables
const CHAT_INGEST_RECEIVE_BUFFER: usize = 10;
const CHAT_INGEST_SEND_BUFFER: usize = 10;
const BROADCAST_BUFFER: usize = 32;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(1);

#[derive(Debug)]
struct ChatBackendHandle {
    id: BackendId,
    receiver: mpsc::Receiver<proto::ChatRequest>,
    sender: broadcast::Sender<proto::Event>,
    cleanup: mpsc::UnboundedSender<CleanupRequest>,
}

impl Drop for ChatBackendHandle {
    fn drop(&mut self) {
        debug!("dropping backend {}", self.id);
        if let Err(err) = self.cleanup.send(CleanupRequest::Backend(self.id.clone())) {
            warn!("failed to notify cleanup of backend {}: {}", self.id, err);
        }
    }
}

#[derive(Debug)]
struct ChatBackend {
    sender: mpsc::Sender<proto::ChatRequest>,
    channels: RwLock<BTreeMap<String, proto::Channel>>,
}

#[derive(Debug)]
struct ChatRequestHandle {
    id: String,
    receiver: oneshot::Receiver<proto::ChatEventInner>,
    cleanup: mpsc::UnboundedSender<CleanupRequest>,
}

impl Drop for ChatRequestHandle {
    fn drop(&mut self) {
        debug!("dropping request {}", self.id);
        if let Err(err) = self.cleanup.send(CleanupRequest::Request(self.id.clone())) {
            warn!("failed to notify cleanup of request {}: {}", self.id, err);
        }
    }
}

#[derive(Debug)]
struct CommandsHandle {
    commands: Vec<String>,
    cleanup: mpsc::UnboundedSender<CleanupRequest>,
}

impl Drop for CommandsHandle {
    fn drop(&mut self) {
        debug!("dropping commands {:?}", self.commands);
        if let Err(err) = self
            .cleanup
            .send(CleanupRequest::Commands(self.commands.clone()))
        {
            warn!(
                "failed to notify cleanup of commands {:?}: {}",
                self.commands, err
            );
        }
    }
}

#[derive(Debug)]
struct ChatRequest {
    sender: oneshot::Sender<proto::ChatEventInner>,
}

#[derive(Debug)]
pub enum CleanupRequest {
    Request(String),
    Backend(BackendId),
    Commands(Vec<String>),
}

#[derive(Debug)]
pub struct Server {
    bind_host: String,
    startup_timestamp: u64,
    sender: broadcast::Sender<proto::Event>,
    requests: Mutex<BTreeMap<String, Option<ChatRequest>>>,
    backends: RwLock<BTreeMap<BackendId, Arc<ChatBackend>>>,
    tokens: RwLock<BTreeMap<String, String>>,
    commands: Arc<RwLock<BTreeMap<String, proto::CommandMetadata>>>,

    cleanup_receiver: Mutex<mpsc::UnboundedReceiver<CleanupRequest>>,
    cleanup_sender: mpsc::UnboundedSender<CleanupRequest>,
}

impl Server {
    pub fn new(bind_host: String) -> Result<Arc<Self>> {
        // We actually don't care about the receiving side - the clients will
        // subscribe to it later.
        let (sender, _) = broadcast::channel(BROADCAST_BUFFER);
        let (cleanup_sender, cleanup_receiver) = mpsc::unbounded_channel();

        Ok(Arc::new(Server {
            bind_host,
            startup_timestamp: SystemTime::now().duration_since(UNIX_EPOCH)?.as_secs(),
            sender,
            requests: Mutex::new(BTreeMap::new()),
            backends: RwLock::new(BTreeMap::new()),
            tokens: RwLock::new(BTreeMap::new()),
            commands: Arc::new(RwLock::new(BTreeMap::new())),
            cleanup_receiver: Mutex::new(cleanup_receiver),
            cleanup_sender,
        }))
    }

    pub async fn set_tokens(&self, tokens: BTreeMap<String, String>) {
        let mut tokens_guard = self.tokens.write().await;
        *tokens_guard = tokens;
    }

    pub async fn run(self: &Arc<Self>) -> Result<()> {
        let server = self.clone();
        tokio::try_join!(server.run_grpc_server(), self.cleanup_task())?;
        Ok(())
    }
}

impl Server {
    async fn cleanup_task(&self) -> Result<()> {
        let mut cleanup_receiver = self.cleanup_receiver.try_lock()?;

        loop {
            match cleanup_receiver.recv().await {
                Some(CleanupRequest::Backend(backend_id)) => {
                    debug!("cleaning backend {}", backend_id);
                    let mut backends = self.backends.write().await;
                    if backends.remove(&backend_id).is_none() {
                        warn!("tried to clean backend {} but it didn't exist", backend_id);
                    };
                }
                Some(CleanupRequest::Request(request_id)) => {
                    debug!("cleaning request {}", request_id);
                    let mut requests = self.requests.lock().await;
                    if requests.remove(&request_id).is_none() {
                        warn!("tried to clean request {} but it didn't exist", request_id);
                    };
                }
                Some(CleanupRequest::Commands(cleanup_commands)) => {
                    info!("cleaning {} command(s)", cleanup_commands.len());
                    let mut commands = self.commands.write().await;
                    for name in cleanup_commands.into_iter() {
                        if commands.remove(&name).is_none() {
                            warn!("tried to clean command {} but it didn't exist", name);
                        };
                    }
                }
                None => {
                    anyhow::bail!("cleanup_receiver closed unexpectedly")
                }
            }
        }
    }

    async fn register_backend(
        &self,
        id: &BackendId,
    ) -> RpcResult<(ChatBackendHandle, Arc<ChatBackend>)> {
        let mut backends = self.backends.write().await;

        if backends.contains_key(id) {
            return Err(Status::already_exists("id already exists"));
        }

        let (sender, receiver) = mpsc::channel(CHAT_INGEST_RECEIVE_BUFFER);

        let handle = ChatBackendHandle {
            id: id.clone(),
            receiver,
            sender: self.sender.clone(),
            cleanup: self.cleanup_sender.clone(),
        };
        let backend = Arc::new(ChatBackend {
            sender,
            channels: RwLock::new(BTreeMap::new()),
        });

        backends.insert(id.clone(), backend.clone());

        Ok((handle, backend))
    }

    async fn respond(&self, id: &str, event: proto::ChatEventInner) {
        let mut requests = self.requests.lock().await;

        match requests.remove(id) {
            Some(Some(handle)) => {
                debug!("responding to request {}", id);
                if let Err(err) = handle.sender.send(event) {
                    warn!("failed to respond to request {}: {:?}", id, err);
                }
                requests.insert(id.to_string(), None);
            }
            Some(None) => {
                warn!("request has already been responded to {}", id);
            }
            None => {}
        }
    }

    async fn issue_request(
        &self,
        backend_id: BackendId,
        req: proto::ChatRequestInner,
    ) -> RpcResult<ChatEventInner> {
        let request_id = uuid::Uuid::new_v4().to_string();

        // This needs to be done in a block to ensure the locks for requests and
        // backends properly get dropped before waiting for a response.
        let mut handle = {
            let mut requests = self.requests.lock().await;

            debug!("issuing request {}", request_id);

            if requests.contains_key(&request_id) {
                return Err(Status::internal("failed to generate unique request ID"));
            }

            let (sender, receiver) = oneshot::channel();
            requests.insert(request_id.clone(), Some(ChatRequest { sender }));

            let backends = self.backends.read().await;
            let backend_sender = backends
                .get(&backend_id)
                .ok_or_else(|| Status::not_found("unknown backend"))?
                .sender
                .clone();

            backend_sender
                .clone()
                .send(proto::ChatRequest {
                    id: request_id.clone(),
                    inner: Some(req),
                })
                .await
                .map_err(|_| Status::internal("failed to send request"))?;

            ChatRequestHandle {
                id: request_id.clone(),
                receiver,
                cleanup: self.cleanup_sender.clone(),
            }
        };

        debug!("waiting for request response {}", request_id);

        let resp = tokio::time::timeout(REQUEST_TIMEOUT, &mut handle.receiver)
            .await
            .map_err(|_| Status::deadline_exceeded("request timed out"))?
            .map_err(|_| Status::internal("request channel closed"))?;

        debug!("got request response for {}: {:?}", request_id, resp);

        Ok(resp)
    }

    async fn run_grpc_server(self: &Arc<Self>) -> Result<()> {
        let addr = self.bind_host.parse()?;

        let chat_ingest =
            auth::AuthedService::new(self.clone(), ChatIngestServer::new(self.clone()));

        let seabird = auth::AuthedService::new(self.clone(), SeabirdServer::new(self.clone()));

        tonic::transport::Server::builder()
            .add_service(seabird)
            .add_service(chat_ingest)
            .serve(addr)
            .await?;

        anyhow::bail!("run_grpc_server exited early");
    }

    fn broadcast(self: &Arc<Self>, inner: EventInner, tags: HashMap<String, String>) {
        // We ignore send errors because they're not relevant to us here. The
        // only time this will return an error is if all the receivers have been
        // dropped which is a valid state because a client can call .subscribe
        // at any time.
        let _ = self.sender.send(proto::Event {
            inner: Some(inner),
            tags,
        });
    }
}
