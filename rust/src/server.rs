use std::collections::BTreeMap;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::future::{select, Either};
use futures::StreamExt;
use hyper::{Body, Request as HyperRequest, Response as HyperResponse};
use tokio::sync::{broadcast, mpsc, oneshot, Mutex, RwLock};
use tonic::{body::BoxBody, transport::NamedService, Request, Response};
use tower::Service;

use crate::prelude::*;

use proto::seabird::seabird_server::{Seabird, SeabirdServer};
use proto::{
    seabird::chat_ingest_server::{ChatIngest, ChatIngestServer},
    ChatEventInner, EventInner,
};

// TODO: make these configurable using environment variables
const CHAT_INGEST_SEND_BUFFER: usize = 10;
const CHAT_INGEST_RECEIVE_BUFFER: usize = 10;
const BROADCAST_BUFFER: usize = 32;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(1);

const X_AUTH_TAG: &str = "x-auth-tag";

fn extract_auth_tag<T>(req: &Request<T>) -> RpcResult<String> {
    Ok(req
        .metadata()
        .get(X_AUTH_TAG)
        .ok_or_else(|| Status::internal("missing auth tag"))?
        .to_str()
        .map_err(|_| Status::internal("failed to decode auth tag"))?
        .to_string())
}

// AuthedService is a frustratingly necessary evil because there is no async
// tonic::Interceptor. This means we can't .await on the RwLock protecting
// server.tokens. We get around this by implementing a middleware which pulls
// the request header out (since the http2 headers map to gRPC request metadata
// directly) to check it before we even get into the gRPC/Tonic code.
#[derive(Debug, Clone)]
struct AuthedService<S> {
    server: Arc<Server>,
    inner: S,
}

impl<S> Service<HyperRequest<Body>> for AuthedService<S>
where
    S: Service<HyperRequest<Body>, Response = HyperResponse<BoxBody>>
        + NamedService
        + Clone
        + Send
        + 'static,
    S::Future: Send + 'static,
{
    type Response = S::Response;
    type Error = S::Error;
    type Future = futures::future::BoxFuture<'static, Result<Self::Response, Self::Error>>;

    fn poll_ready(
        &mut self,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }

    fn call(&mut self, req: HyperRequest<Body>) -> Self::Future {
        let mut svc = self.inner.clone();
        let server = self.server.clone();

        Box::pin(async move {
            let (mut req, tag): (HyperRequest<Body>, _) = match req.headers().get("authorization") {
                Some(token) => {
                    let token = match token.to_str() {
                        Ok(token) => token,
                        Err(_) => {
                            return Ok(Status::unauthenticated("invalid token data").to_http())
                        }
                    };

                    let mut split = token.splitn(2, ' ');
                    match (split.next(), split.next()) {
                        (Some("Bearer"), Some(token)) => {
                            let tokens = server.tokens.read().await;
                            match tokens.get(token) {
                                Some(val) => match val.parse() {
                                    Ok(tag) => (req, tag),
                                    Err(_) => {
                                        return Ok(
                                            Status::internal("invalid auth token tag").to_http()
                                        )
                                    }
                                },
                                None => {
                                    return Ok(
                                        Status::unauthenticated("invalid auth token").to_http()
                                    )
                                }
                            }
                        }
                        (Some("Bearer"), None) => {
                            return Ok(Status::unauthenticated("missing auth token").to_http())
                        }
                        (Some(_), _) => {
                            return Ok(Status::unauthenticated("unknown auth method").to_http())
                        }
                        (None, _) => {
                            return Ok(Status::unauthenticated("missing auth method").to_http())
                        }
                    }
                }
                None => return Ok(Status::unauthenticated("missing authorization").to_http()),
            };

            req.headers_mut().insert(X_AUTH_TAG, tag);

            info!("Authenticated request: {}", req.uri());

            let resp = svc.call(req).await?;

            info!("Sending response: {:?}", resp.headers());

            Ok(resp)
        })
    }
}

impl<S: NamedService> NamedService for AuthedService<S> {
    const NAME: &'static str = S::NAME;
}

#[derive(Debug)]
struct ChatBackendHandle {
    id: BackendId,
    receiver: mpsc::Receiver<proto::ChatRequest>,
    sender: broadcast::Sender<proto::Event>,
    cleanup: mpsc::UnboundedSender<BackendId>,
}

impl Drop for ChatBackendHandle {
    fn drop(&mut self) {
        debug!("dropping backend {}", self.id);
        if let Err(err) = self.cleanup.send(self.id.clone()) {
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
    cleanup: mpsc::UnboundedSender<String>,
}

impl Drop for ChatRequestHandle {
    fn drop(&mut self) {
        debug!("dropping request {}", self.id);
        if let Err(err) = self.cleanup.send(self.id.clone()) {
            warn!("failed to notify cleanup of request {}: {}", self.id, err);
        }
    }
}

#[derive(Debug)]
struct ChatRequest {
    sender: oneshot::Sender<proto::ChatEventInner>,
}

#[derive(Debug)]
pub struct Server {
    bind_host: String,
    startup_timestamp: u64,
    sender: broadcast::Sender<proto::Event>,
    requests: Mutex<BTreeMap<String, Option<ChatRequest>>>,
    backends: RwLock<BTreeMap<BackendId, Arc<ChatBackend>>>,
    tokens: RwLock<BTreeMap<String, String>>,

    cleanup_request_receiver: Mutex<mpsc::UnboundedReceiver<String>>,
    cleanup_request_sender: mpsc::UnboundedSender<String>,
    cleanup_backend_receiver: Mutex<mpsc::UnboundedReceiver<BackendId>>,
    cleanup_backend_sender: mpsc::UnboundedSender<BackendId>,
}

impl Server {
    pub fn new(bind_host: String) -> Result<Arc<Self>> {
        // We actually don't care about the receiving side - the clients will
        // subscribe to it later.
        let (sender, _) = broadcast::channel(BROADCAST_BUFFER);
        let (cleanup_request_sender, cleanup_request_receiver) = mpsc::unbounded_channel();
        let (cleanup_backend_sender, cleanup_backend_receiver) = mpsc::unbounded_channel();

        Ok(Arc::new(Server {
            bind_host,
            startup_timestamp: SystemTime::now().duration_since(UNIX_EPOCH)?.as_secs(),
            sender,
            requests: Mutex::new(BTreeMap::new()),
            backends: RwLock::new(BTreeMap::new()),
            tokens: RwLock::new(BTreeMap::new()),
            cleanup_request_receiver: Mutex::new(cleanup_request_receiver),
            cleanup_request_sender,
            cleanup_backend_receiver: Mutex::new(cleanup_backend_receiver),
            cleanup_backend_sender,
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
        let mut cleanup_backend_receiver = self.cleanup_backend_receiver.try_lock()?;
        let mut cleanup_request_receiver = self.cleanup_request_receiver.try_lock()?;

        loop {
            // NOTE: for some reason, the receiver's recv() future doesn't
            // implement Unpin, so select doesn't work properly.
            match select(
                cleanup_backend_receiver.next(),
                cleanup_request_receiver.next(),
            )
            .await
            {
                Either::Left((Some(backend_id), _)) => {
                    debug!("cleaning backend {}", backend_id);
                    let mut backends = self.backends.write().await;
                    if let None = backends.remove(&backend_id) {
                        warn!("tried to clean backend {} but it didn't exist", backend_id);
                    };
                }
                Either::Right((Some(request_id), _)) => {
                    debug!("cleaning request {}", request_id);
                    let mut requests = self.requests.lock().await;
                    if let None = requests.remove(&request_id) {
                        warn!("tried to clean request {} but it didn't exist", request_id);
                    };
                }
                Either::Left((None, _)) => {
                    anyhow::bail!("cleanup_backend_receiver closed unexpectedly")
                }
                Either::Right((None, _)) => {
                    anyhow::bail!("cleanup_request_receiver closed unexpectedly")
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
            cleanup: self.cleanup_backend_sender.clone(),
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
                cleanup: self.cleanup_request_sender.clone(),
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

        let chat_ingest = AuthedService {
            server: self.clone(),
            inner: ChatIngestServer::new(self.clone()),
        };

        let seabird = AuthedService {
            server: self.clone(),
            inner: SeabirdServer::new(self.clone()),
        };

        tonic::transport::Server::builder()
            .add_service(seabird)
            .add_service(chat_ingest)
            .serve(addr)
            .await?;

        anyhow::bail!("run_grpc_server exited early");
    }

    fn broadcast(self: &Arc<Self>, inner: EventInner) {
        // We ignore send errors because they're not relevant to us here
        let _ = self.sender.send(proto::Event { inner: Some(inner) });
    }
}

#[async_trait]
impl ChatIngest for Arc<Server> {
    type IngestEventsStream = mpsc::Receiver<RpcResult<proto::ChatRequest>>;

    async fn ingest_events(
        &self,
        request: Request<tonic::Streaming<proto::ChatEvent>>,
    ) -> RpcResult<Response<Self::IngestEventsStream>> {
        let mut input_stream = request.into_inner();
        let (mut sender, receiver) = mpsc::channel(CHAT_INGEST_SEND_BUFFER);
        let server = self.clone();

        // Spawn a task to handle the stream
        crate::spawn(sender.clone(), async move {
            let hello = {
                // TODO: send to the stream and print
                let first_message = input_stream
                    .message()
                    .await?
                    .ok_or_else(|| Status::invalid_argument("missing hello message"))?;

                match first_message.inner {
                    Some(ChatEventInner::Hello(hello)) => hello,
                    Some(_) => {
                        return Err(Status::invalid_argument(
                            "hello message inner is wrong type",
                        ))
                    }
                    None => return Err(Status::invalid_argument("missing hello message inner")),
                }
            };

            let backend_info = hello
                .backend_info
                .ok_or_else(|| Status::invalid_argument("missing backend_info inner"))?;

            let id = BackendId::new(backend_info.r#type, backend_info.id);

            let (mut backend_handle, backend) = server.register_backend(&id).await?;

            loop {
                match select(backend_handle.receiver.next(), input_stream.next()).await {
                    Either::Left((Some(req), _)) => {
                        debug!("got request: {:?}", req);

                        sender
                            .send(Ok(req))
                            .await
                            .map_err(|_| Status::internal("failed to send event to backend"))?;
                    }
                    Either::Right((Some(event), _)) => {
                        let event = event?;

                        debug!("got event: {:?}", event);

                        let inner = event
                            .inner
                            .ok_or_else(|| Status::internal("missing inner event"))?;

                        if event.id != "" {
                            server.respond(&event.id, inner.clone()).await;
                        }

                        match inner {
                            // Success/Failed are only used for responding to
                            // requests, so we ignore them here because they
                            // don't need to be handled specially.
                            ChatEventInner::Success(_) => {}
                            ChatEventInner::Failed(_) => {}

                            ChatEventInner::Action(action) => {
                                let _ = backend_handle.sender.send(proto::Event {
                                    inner: Some(EventInner::Action(proto::ActionEvent {
                                        source: action
                                            .source
                                            .map(|source| source.into_relative(&id)),
                                        text: action.text,
                                    })),
                                });
                            }
                            ChatEventInner::PrivateAction(private_action) => {
                                let _ = backend_handle.sender.send(proto::Event {
                                    inner: Some(EventInner::PrivateAction(
                                        proto::PrivateActionEvent {
                                            source: private_action
                                                .source
                                                .map(|source| source.into_relative(&id)),
                                            text: private_action.text,
                                        },
                                    )),
                                });
                            }
                            ChatEventInner::Message(msg) => {
                                let _ = backend_handle.sender.send(proto::Event {
                                    inner: Some(EventInner::Message(proto::MessageEvent {
                                        source: msg.source.map(|source| source.into_relative(&id)),
                                        text: msg.text,
                                    })),
                                });
                            }
                            ChatEventInner::PrivateMessage(private_msg) => {
                                let _ = backend_handle.sender.send(proto::Event {
                                    inner: Some(EventInner::PrivateMessage(
                                        proto::PrivateMessageEvent {
                                            source: private_msg
                                                .source
                                                .map(|user| user.into_relative(&id)),
                                            text: private_msg.text,
                                        },
                                    )),
                                });
                            }
                            ChatEventInner::Command(cmd_msg) => {
                                let _ = backend_handle.sender.send(proto::Event {
                                    inner: Some(EventInner::Command(proto::CommandEvent {
                                        source: cmd_msg
                                            .source
                                            .map(|source| source.into_relative(&id)),
                                        command: cmd_msg.command,
                                        arg: cmd_msg.arg,
                                    })),
                                });
                            }
                            ChatEventInner::Mention(mention_msg) => {
                                let _ = backend_handle.sender.send(proto::Event {
                                    inner: Some(EventInner::Mention(proto::MentionEvent {
                                        source: mention_msg
                                            .source
                                            .map(|source| source.into_relative(&id)),
                                        text: mention_msg.text,
                                    })),
                                });
                            }

                            ChatEventInner::JoinChannel(join) => {
                                let mut channels = backend.channels.write().await;

                                channels.insert(
                                    join.channel_id.clone(),
                                    proto::Channel {
                                        id: join.channel_id,
                                        display_name: join.display_name,
                                        topic: join.topic,
                                    },
                                );
                            }
                            ChatEventInner::LeaveChannel(leave) => {
                                let mut channels = backend.channels.write().await;

                                channels.remove(&leave.channel_id);
                            }
                            ChatEventInner::ChangeChannel(change) => {
                                let mut channels = backend.channels.write().await;

                                if let Some(channel) = channels.get_mut(&change.channel_id) {
                                    channel.display_name = change.display_name;
                                    channel.topic = change.topic;
                                }
                            }

                            // If a hello comes through, this client has broken
                            // the protocol contract, so we kill the connection.
                            ChatEventInner::Hello(_) => {
                                return Err(Status::invalid_argument("unexpected chat event type"))
                            }
                        }
                    }
                    Either::Left((None, _)) => {
                        return Err(Status::internal("internal request stream ended"))
                    }
                    Either::Right((None, _)) => {
                        return Err(Status::internal("chat event stream ended"))
                    }
                };
            }
        });

        Ok(tonic::Response::new(receiver))
    }
}

#[async_trait]
impl Seabird for Arc<Server> {
    type StreamEventsStream = mpsc::Receiver<RpcResult<proto::Event>>;

    async fn stream_events(
        &self,
        _req: Request<proto::StreamEventsRequest>,
    ) -> RpcResult<Response<Self::StreamEventsStream>> {
        // TODO: properly use req

        let (mut sender, receiver) = mpsc::channel(CHAT_INGEST_SEND_BUFFER);

        let mut events = self.sender.subscribe();

        // Spawn a task to handle the stream
        crate::spawn(sender.clone(), async move {
            loop {
                let event = events
                    .recv()
                    .await
                    .map_err(|_| Status::internal("failed to read internal event"))?;

                sender
                    .send(Ok(event))
                    .await
                    .map_err(|err| Status::internal(format!("failed to send event: {}", err)))?;
            }
        });

        Ok(Response::new(receiver))
    }

    async fn send_message(
        &self,
        req: Request<proto::SendMessageRequest>,
    ) -> RpcResult<Response<proto::SendMessageResponse>> {
        let tag = extract_auth_tag(&req)?;
        let req = req.into_inner();
        let (backend_id, channel_id) = req
            .channel_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse channel_id"))?
            .into_inner();

        self.broadcast(EventInner::SendMessage(proto::SendMessageEvent {
            sender: tag.to_string(),
            inner: Some(proto::SendMessageRequest {
                channel_id: req.channel_id,
                text: req.text.clone(),
            }),
        }));

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::SendMessage(proto::SendMessageChatRequest {
                    channel_id,
                    text: req.text,
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
        let tag = extract_auth_tag(&req)?;
        let req = req.into_inner();
        let (backend_id, user_id) = req
            .user_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse user_id"))?
            .into_inner();

        self.broadcast(EventInner::SendPrivateMessage(
            proto::SendPrivateMessageEvent {
                sender: tag.to_string(),
                inner: Some(proto::SendPrivateMessageRequest {
                    user_id: req.user_id,
                    text: req.text.clone(),
                }),
            },
        ));

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::SendPrivateMessage(proto::SendPrivateMessageChatRequest {
                    user_id,
                    text: req.text,
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
        let tag = extract_auth_tag(&req)?;
        let req = req.into_inner();
        let (backend_id, channel_id) = req
            .channel_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse channel_id"))?
            .into_inner();

        self.broadcast(EventInner::PerformAction(proto::PerformActionEvent {
            sender: tag.to_string(),
            inner: Some(proto::PerformActionRequest {
                channel_id: req.channel_id,
                text: req.text.clone(),
            }),
        }));

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::PerformAction(proto::PerformActionChatRequest {
                    channel_id,
                    text: req.text,
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
        let tag = extract_auth_tag(&req)?;
        let req = req.into_inner();
        let (backend_id, user_id) = req
            .user_id
            .parse::<FullId>()
            .map_err(|_| Status::invalid_argument("failed to parse user_id"))?
            .into_inner();

        self.broadcast(EventInner::PerformPrivateAction(
            proto::PerformPrivateActionEvent {
                sender: tag.to_string(),
                inner: Some(proto::PerformPrivateActionRequest {
                    user_id: req.user_id,
                    text: req.text.clone(),
                }),
            },
        ));

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::PerformPrivateAction(
                    proto::PerformPrivateActionChatRequest {
                        user_id,
                        text: req.text,
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

        Ok(Response::new(proto::BackendInfoResponse {
            backend: Some(proto::Backend {
                id: backend_id.to_string(),
                r#type: backend_id.scheme.clone(),
            }),
        }))
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
        }))
    }
}
