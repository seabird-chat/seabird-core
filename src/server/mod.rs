use std::collections::BTreeMap;
use std::collections::HashMap;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::future::{select, Either};
use futures::StreamExt;
use tokio::sync::{broadcast, mpsc, oneshot, Mutex, RwLock};
use tonic::{Request, Response};

use crate::prelude::*;

use proto::{
    seabird::chat_ingest_server::{ChatIngest, ChatIngestServer},
    seabird::seabird_server::{Seabird, SeabirdServer},
    ChatEventInner, EventInner,
};

mod auth;
mod ingest_events;

use auth::extract_auth_username;
use ingest_events::IngestEventsHandle;

// TODO: make these configurable using environment variables
const CHAT_INGEST_RECEIVE_BUFFER: usize = 10;
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
        futures::future::try_join(self.clone().run_grpc_server(), self.cleanup_task()).await?;
        Ok(())
    }
}

impl Server {
    async fn cleanup_task(&self) -> Result<()> {
        let mut cleanup_receiver = self.cleanup_receiver.try_lock()?;

        loop {
            match cleanup_receiver.next().await {
                Some(CleanupRequest::Backend(backend_id)) => {
                    debug!("cleaning backend {}", backend_id);
                    let mut backends = self.backends.write().await;
                    if let None = backends.remove(&backend_id) {
                        warn!("tried to clean backend {} but it didn't exist", backend_id);
                    };
                }
                Some(CleanupRequest::Request(request_id)) => {
                    debug!("cleaning request {}", request_id);
                    let mut requests = self.requests.lock().await;
                    if let None = requests.remove(&request_id) {
                        warn!("tried to clean request {} but it didn't exist", request_id);
                    };
                }
                Some(CleanupRequest::Commands(cleanup_commands)) => {
                    info!("cleaning {} command(s)", cleanup_commands.len());
                    let mut commands = self.commands.write().await;
                    for name in cleanup_commands.into_iter() {
                        if let None = commands.remove(&name) {
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

#[async_trait]
impl ChatIngest for Arc<Server> {
    type IngestEventsStream = IngestEventsHandle;

    async fn ingest_events(
        &self,
        request: Request<tonic::Streaming<proto::ChatEvent>>,
    ) -> RpcResult<Response<Self::IngestEventsStream>> {
        let input_stream = request.into_inner();
        let server = self.clone();

        Ok(tonic::Response::new(IngestEventsHandle::new(
            server,
            input_stream,
        )))
    }
}

#[async_trait]
impl Seabird for Arc<Server> {
    type StreamEventsStream = crate::wrapped::WrappedChannelReceiver<RpcResult<proto::Event>>;

    async fn stream_events(
        &self,
        req: Request<proto::StreamEventsRequest>,
    ) -> RpcResult<Response<Self::StreamEventsStream>> {
        let req = req.into_inner();

        // Track registered commands
        let mut commands = self.commands.write().await;
        let mut to_cleanup = Vec::new();
        for (name, metadata) in req.commands.into_iter() {
            if commands.contains_key(&name) {
                return Err(Status::already_exists(format!(
                    "command \"{}\" already registered by another plugin",
                    name
                )));
            }

            to_cleanup.push(name.clone());
            commands.insert(name, metadata);
        }

        let (mut sender, receiver, mut notifier) =
            crate::wrapped::channel(CHAT_INGEST_RECEIVE_BUFFER);

        let mut events = self.sender.subscribe();

        let cleanup_sender = self.cleanup_sender.clone();

        // Spawn a task to handle the stream
        crate::spawn(async move {
            let _handle = CommandsHandle {
                commands: to_cleanup,
                cleanup: cleanup_sender,
            };

            loop {
                match select(events.next(), &mut notifier).await {
                    Either::Left((Some(Ok(event)), _)) => sender
                        .send(Ok(event))
                        .await
                        .map_err(|err| Status::internal(format!("failed to send event: {}", err))),
                    Either::Left((Some(Err(err)), _)) => Err(Status::internal(format!(
                        "failed to read internal event: {}",
                        err
                    ))),
                    Either::Left((None, _)) => {
                        // Stream was closed by the server. In the future, maybe
                        // this could be used to notify client streams that
                        // seabird-core is restarting.
                        Err(Status::internal(
                            "input stream ended unexpectedly".to_string(),
                        ))
                    }
                    Either::Right((_, _)) => {
                        // Stream was closed by the client - this is not actually an error.
                        return Ok(());
                    }
                }?;
            }
        });

        Ok(Response::new(receiver))
    }

    async fn send_message(
        &self,
        req: Request<proto::SendMessageRequest>,
    ) -> RpcResult<Response<proto::SendMessageResponse>> {
        let username = extract_auth_username(&req)?;
        let req = req.into_inner();
        let (backend_id, channel_id) = req
            .channel_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse channel_id"))?
            .into_inner();

        self.broadcast(
            EventInner::SendMessage(proto::SendMessageEvent {
                sender: username.to_string(),
                channel_id: req.channel_id,
                text: req.text.clone(),
            }),
            req.tags.clone(),
        );

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::SendMessage(proto::SendMessageChatRequest {
                    channel_id,
                    text: req.text,
                    tags: req.tags,
                }),
            )
            .await?;

        match resp {
            ChatEventInner::Success(_) => Ok(Response::new(proto::SendMessageResponse {})),
            ChatEventInner::Failed(failed) => Err(Status::unknown(failed.reason)),
            _ => Err(Status::internal("unexpected chat event")),
        }
    }

    async fn send_private_message(
        &self,
        req: Request<proto::SendPrivateMessageRequest>,
    ) -> RpcResult<Response<proto::SendPrivateMessageResponse>> {
        let username = extract_auth_username(&req)?;
        let req = req.into_inner();
        let (backend_id, user_id) = req
            .user_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse user_id"))?
            .into_inner();

        self.broadcast(
            EventInner::SendPrivateMessage(proto::SendPrivateMessageEvent {
                sender: username.to_string(),
                user_id: req.user_id,
                text: req.text.clone(),
            }),
            req.tags.clone(),
        );

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::SendPrivateMessage(proto::SendPrivateMessageChatRequest {
                    user_id,
                    text: req.text,
                    tags: req.tags,
                }),
            )
            .await?;

        match resp {
            ChatEventInner::Success(_) => Ok(Response::new(proto::SendPrivateMessageResponse {})),
            ChatEventInner::Failed(failed) => Err(Status::unknown(failed.reason)),
            _ => Err(Status::internal("unexpected chat event")),
        }
    }

    async fn perform_action(
        &self,
        req: Request<proto::PerformActionRequest>,
    ) -> RpcResult<Response<proto::PerformActionResponse>> {
        let username = extract_auth_username(&req)?;
        let req = req.into_inner();
        let (backend_id, channel_id) = req
            .channel_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse channel_id"))?
            .into_inner();

        self.broadcast(
            EventInner::PerformAction(proto::PerformActionEvent {
                sender: username.to_string(),
                channel_id: req.channel_id,
                text: req.text.clone(),
            }),
            req.tags.clone(),
        );

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::PerformAction(proto::PerformActionChatRequest {
                    channel_id,
                    text: req.text,
                    tags: req.tags,
                }),
            )
            .await?;

        match resp {
            ChatEventInner::Success(_) => Ok(Response::new(proto::PerformActionResponse {})),
            ChatEventInner::Failed(failed) => Err(Status::unknown(failed.reason)),
            _ => Err(Status::internal("unexpected chat event")),
        }
    }

    async fn perform_private_action(
        &self,
        req: Request<proto::PerformPrivateActionRequest>,
    ) -> RpcResult<Response<proto::PerformPrivateActionResponse>> {
        let username = extract_auth_username(&req)?;
        let req = req.into_inner();
        let (backend_id, user_id) = req
            .user_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse user_id"))?
            .into_inner();

        self.broadcast(
            EventInner::PerformPrivateAction(proto::PerformPrivateActionEvent {
                sender: username.to_string(),
                user_id: req.user_id,
                text: req.text.clone(),
            }),
            req.tags.clone(),
        );

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::PerformPrivateAction(
                    proto::PerformPrivateActionChatRequest {
                        user_id,
                        text: req.text,
                        tags: req.tags,
                    },
                ),
            )
            .await?;

        match resp {
            ChatEventInner::Success(_) => Ok(Response::new(proto::PerformPrivateActionResponse {})),
            ChatEventInner::Failed(failed) => Err(Status::unknown(failed.reason)),
            _ => Err(Status::internal("unexpected chat event")),
        }
    }

    async fn join_channel(
        &self,
        req: Request<proto::JoinChannelRequest>,
    ) -> RpcResult<Response<proto::JoinChannelResponse>> {
        let req = req.into_inner();
        let backend_id: BackendId = req
            .backend_id
            .parse()
            .map_err(|_| Status::invalid_argument("failed to parse backend_id"))?;

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::JoinChannel(proto::JoinChannelChatRequest {
                    channel_name: req.channel_name,
                    tags: req.tags,
                }),
            )
            .await?;

        match resp {
            ChatEventInner::Success(_) | ChatEventInner::JoinChannel(_) => {
                Ok(Response::new(proto::JoinChannelResponse {}))
            }
            ChatEventInner::Failed(failed) => Err(Status::unknown(failed.reason)),
            _ => Err(Status::internal("unexpected chat event")),
        }
    }

    async fn leave_channel(
        &self,
        req: Request<proto::LeaveChannelRequest>,
    ) -> RpcResult<Response<proto::LeaveChannelResponse>> {
        let req = req.into_inner();
        let (backend_id, channel_id) = req
            .channel_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse channel_id"))?
            .into_inner();

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::LeaveChannel(proto::LeaveChannelChatRequest {
                    channel_id,
                    tags: req.tags,
                }),
            )
            .await?;

        match resp {
            ChatEventInner::Success(_) | ChatEventInner::LeaveChannel(_) => {
                Ok(Response::new(proto::LeaveChannelResponse {}))
            }
            ChatEventInner::Failed(failed) => Err(Status::unknown(failed.reason)),
            _ => Err(Status::internal("unexpected chat event")),
        }
    }

    async fn update_channel_info(
        &self,
        req: Request<proto::UpdateChannelInfoRequest>,
    ) -> RpcResult<Response<proto::UpdateChannelInfoResponse>> {
        let req = req.into_inner();
        let (backend_id, channel_id) = req
            .channel_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse channel_id"))?
            .into_inner();

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::UpdateChannelInfo(proto::UpdateChannelInfoChatRequest {
                    channel_id,
                    topic: req.topic,
                    tags: req.tags,
                }),
            )
            .await?;

        match resp {
            ChatEventInner::Success(_) | ChatEventInner::ChangeChannel(_) => {
                Ok(Response::new(proto::UpdateChannelInfoResponse {}))
            }
            ChatEventInner::Failed(failed) => Err(Status::unknown(failed.reason)),
            _ => Err(Status::internal("unexpected chat event")),
        }
    }

    async fn list_backends(
        &self,
        req: Request<proto::ListBackendsRequest>,
    ) -> RpcResult<Response<proto::ListBackendsResponse>> {
        let _req = req.into_inner();

        let backends = self.backends.read().await;

        Ok(Response::new(proto::ListBackendsResponse {
            backends: backends
                .iter()
                .map(|(k, _)| proto::Backend {
                    id: k.to_string(),
                    r#type: k.scheme.clone(),
                })
                .collect(),
        }))
    }

    async fn get_backend_info(
        &self,
        req: Request<proto::BackendInfoRequest>,
    ) -> RpcResult<Response<proto::BackendInfoResponse>> {
        let req = req.into_inner();

        let backend_id: BackendId = req
            .backend_id
            .parse()
            .map_err(|_| Status::invalid_argument("failed to parse backend_id"))?;

        let backends = self.backends.read().await;
        let _backend = backends
            .get(&backend_id)
            .ok_or_else(|| Status::not_found("backend not found"))?;

        let resp = self
            .issue_request(
                backend_id.clone(),
                proto::ChatRequestInner::Metadata(proto::MetadataChatRequest {}),
            )
            .await?;

        match resp {
            ChatEventInner::Metadata(metadata) => Ok(Response::new(proto::BackendInfoResponse {
                backend: Some(proto::Backend {
                    id: backend_id.to_string(),
                    r#type: backend_id.scheme.clone(),
                }),
                metadata: metadata.values,
            })),
            ChatEventInner::Failed(failed) => Err(Status::unknown(failed.reason)),
            _ => Err(Status::internal("unexpected chat event")),
        }
    }

    async fn list_channels(
        &self,
        req: Request<proto::ListChannelsRequest>,
    ) -> RpcResult<Response<proto::ListChannelsResponse>> {
        let req = req.into_inner();

        let backend_id: BackendId = req
            .backend_id
            .parse()
            .map_err(|_| Status::invalid_argument("failed to parse backend_id"))?;

        let backends = self.backends.read().await;
        let channels = backends
            .get(&backend_id)
            .ok_or_else(|| Status::not_found("backend not found"))?
            .channels
            .read()
            .await;

        Ok(Response::new(proto::ListChannelsResponse {
            channels: channels
                .values()
                .map(|channel| proto::Channel {
                    id: backend_id.relative(channel.id.clone()).to_string(),
                    display_name: channel.display_name.clone(),
                    topic: channel.topic.clone(),
                })
                .collect(),
        }))
    }

    async fn get_channel_info(
        &self,
        req: Request<proto::ChannelInfoRequest>,
    ) -> RpcResult<Response<proto::ChannelInfoResponse>> {
        let req = req.into_inner();

        let (backend_id, channel_id) = req
            .channel_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse channel_id"))?
            .into_inner();

        let backends = self.backends.read().await;
        let channels = backends
            .get(&backend_id)
            .ok_or_else(|| Status::not_found("backend not found"))?
            .channels
            .read()
            .await;

        Ok(Response::new(
            channels
                .get(&channel_id)
                .map(|channel| proto::ChannelInfoResponse {
                    channel: Some(proto::Channel {
                        id: backend_id.relative(channel.id.clone()).to_string(),
                        display_name: channel.display_name.clone(),
                        topic: channel.topic.clone(),
                    }),
                })
                .ok_or_else(|| Status::not_found("channel not found"))?,
        ))
    }

    async fn get_core_info(
        &self,
        req: Request<proto::CoreInfoRequest>,
    ) -> RpcResult<Response<proto::CoreInfoResponse>> {
        let _req = req.into_inner();

        Ok(Response::new(proto::CoreInfoResponse {
            startup_timestamp: self.startup_timestamp,
            current_timestamp: SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .map_err(|_e| Status::not_found("backend not found"))?
                .as_secs(),
        }))
    }

    async fn registered_commands(
        &self,
        req: Request<proto::CommandsRequest>,
    ) -> RpcResult<Response<proto::CommandsResponse>> {
        let _req = req.into_inner();

        Ok(Response::new(proto::CommandsResponse {
            commands: self.commands.read().await.clone().into_iter().collect(),
        }))
    }
}
