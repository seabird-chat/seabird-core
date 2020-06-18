use std::collections::BTreeMap;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::future::{select, Either};
use tokio::sync::{broadcast, mpsc, oneshot, Mutex, RwLock};
use tonic::{Request, Response};

use crate::prelude::*;

use proto::seabird::seabird_server::{Seabird, SeabirdServer};
use proto::{
    seabird::chat_ingest_server::{ChatIngest, ChatIngestServer},
    ChatEventInner, EventInner,
};

const CHAT_INGEST_SEND_BUFFER: usize = 10;
const CHAT_INGEST_RECEIVE_BUFFER: usize = 10;
const BROADCAST_BUFFER: usize = 32;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(1);

#[derive(Debug)]
struct ChatBackend {
    receiver: mpsc::Receiver<proto::ChatRequest>,
    sender: broadcast::Sender<proto::Event>,
    // TODO: add Drop impl which sends message to cleanup thread
}

#[derive(Debug)]
struct ChatBackendHandle {
    sender: mpsc::Sender<proto::ChatRequest>,
    channels: RwLock<BTreeMap<String, proto::Channel>>,
}

#[derive(Debug)]
struct ChatRequest {
    receiver: oneshot::Receiver<proto::ChatEventInner>,
    // TODO: add Drop impl which sends message to cleanup thread
}

#[derive(Debug)]
struct ChatRequestHandle {
    sender: oneshot::Sender<proto::ChatEventInner>,
}

#[derive(Debug)]
pub struct Server {
    bind_host: String,
    startup_timestamp: u64,
    sender: broadcast::Sender<proto::Event>,
    requests: Mutex<BTreeMap<String, ChatRequestHandle>>,
    backends: RwLock<BTreeMap<BackendId, Arc<ChatBackendHandle>>>,
    tokens: RwLock<BTreeMap<String, String>>,
}

impl Server {
    pub fn new(bind_host: String) -> Result<Arc<Self>> {
        // We actually don't care about the receiving side - the clients will
        // subscribe to it later.
        let (sender, _) = broadcast::channel(BROADCAST_BUFFER);

        Ok(Arc::new(Server {
            bind_host,
            startup_timestamp: SystemTime::now().duration_since(UNIX_EPOCH)?.as_secs(),
            sender,
            requests: Mutex::new(BTreeMap::new()),
            backends: RwLock::new(BTreeMap::new()),
            tokens: RwLock::new(BTreeMap::new()),
        }))
    }

    pub async fn set_tokens(&self, tokens: BTreeMap<String, String>) {
        let mut tokens_guard = self.tokens.write().await;
        *tokens_guard = tokens;
    }

    pub async fn run(self: &Arc<Self>) -> Result<()> {
        self.clone().run_grpc_server().await
    }
}

impl Server {
    async fn register_backend(
        &self,
        id: &BackendId,
    ) -> RpcResult<(ChatBackend, Arc<ChatBackendHandle>)> {
        let mut backends = self.backends.write().await;

        if backends.contains_key(id) {
            return Err(Status::new(Code::AlreadyExists, "id already exists"));
        }

        let (sender, receiver) = mpsc::channel(CHAT_INGEST_RECEIVE_BUFFER);

        let backend = ChatBackend {
            receiver,
            sender: self.sender.clone(),
        };
        let handle = Arc::new(ChatBackendHandle {
            sender,
            channels: RwLock::new(BTreeMap::new()),
        });

        backends.insert(id.clone(), handle.clone());

        Ok((backend, handle))
    }

    fn check_auth(&self, req: Request<()>) -> RpcResult<Request<()>> {
        println!("{:?}", req.metadata());

        match req.metadata().get("authorization") {
            Some(token) => {
                if token != "Bearer helloworld" {
                    Err(Status::new(
                        Code::Unauthenticated,
                        "invalid authorization token",
                    ))
                } else {
                    Ok(req)
                }
            }
            None => Err(Status::new(Code::Unauthenticated, "missing authorization")),
        }
    }

    async fn respond(&self, id: &str, event: proto::ChatEventInner) {
        let mut requests = self.requests.lock().await;

        match requests.remove(id) {
            Some(handle) => {
                // TODO: log failures, but don't actually return an error
                let _ = handle.sender.send(event);
            }
            None => {}
        }
    }

    async fn issue_request(
        &self,
        backend_id: BackendId,
        req: proto::ChatRequestInner,
    ) -> RpcResult<ChatEventInner> {
        let mut requests = self.requests.lock().await;

        let request_id = uuid::Uuid::new_v4().to_string();

        if requests.contains_key(&request_id) {
            return Err(Status::new(
                Code::Internal,
                "failed to generate unique request ID",
            ));
        }

        let (sender, receiver) = oneshot::channel();
        requests.insert(request_id.clone(), ChatRequestHandle { sender });
        let handle = ChatRequest { receiver };

        let backends = self.backends.read().await;
        let backend_sender = backends
            .get(&backend_id)
            .ok_or_else(|| Status::new(Code::NotFound, "unknown backend"))?
            .sender
            .clone();

        backend_sender
            .clone()
            .send(proto::ChatRequest {
                id: request_id,
                inner: Some(req),
            })
            .await
            .map_err(|_| Status::new(Code::Internal, "failed to send request"))?;

        let resp = tokio::time::timeout(REQUEST_TIMEOUT, handle.receiver)
            .await
            .map_err(|_| Status::new(Code::DeadlineExceeded, "request timed out"))?
            .map_err(|_| Status::new(Code::Internal, "request channel closed"))?;

        Ok(resp)
    }

    async fn run_grpc_server(self: &Arc<Self>) -> Result<()> {
        let addr = self.bind_host.parse()?;

        // Build a chat ingest server which also checks the authorization token.
        let chat_ingest_auth = self.clone();
        let chat_ingest = ChatIngestServer::with_interceptor(self.clone(), move |req| {
            chat_ingest_auth.check_auth(req)
        });

        let seabird_auth = self.clone();
        let seabird =
            SeabirdServer::with_interceptor(self.clone(), move |req| seabird_auth.check_auth(req));

        tonic::transport::Server::builder()
            .add_service(seabird)
            .add_service(chat_ingest)
            .serve(addr)
            .await?;

        anyhow::bail!("run_grpc_server exited early");
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
                    .ok_or_else(|| Status::new(Code::InvalidArgument, "missing hello message"))?;

                match first_message.inner {
                    Some(ChatEventInner::Hello(hello)) => hello,
                    Some(_) => {
                        return Err(Status::new(
                            Code::InvalidArgument,
                            "hello message inner is wrong type",
                        ))
                    }
                    None => {
                        return Err(Status::new(
                            Code::InvalidArgument,
                            "missing hello message inner",
                        ))
                    }
                }
            };

            let backend_info = hello
                .backend_info
                .ok_or_else(|| Status::new(Code::InvalidArgument, "missing backend_info inner"))?;

            let id = BackendId::new(backend_info.r#type, backend_info.id);

            let (mut backend, backend_handle) = server.register_backend(&id).await?;

            loop {
                match select(backend.receiver.next(), input_stream.next()).await {
                    Either::Left((Some(req), _)) => {
                        println!("Req: {:?}", req);

                        sender.send(Ok(req)).await.map_err(|_| {
                            Status::new(Code::Internal, "failed to send event to backend")
                        })?;
                    }
                    Either::Right((Some(event), _)) => {
                        let event = event?;

                        println!("Event: {:?}", event);

                        let inner = event
                            .inner
                            .ok_or_else(|| Status::new(Code::Internal, "missing inner event"))?;

                        if event.id != "" {
                            server.respond(&event.id, inner.clone()).await;
                        }

                        match inner {
                            // Success/Failed are only used for responding to
                            // requests, so we ignore them here because they
                            // don't need to be handled specially.
                            ChatEventInner::Success(_) => {}
                            ChatEventInner::Failed(_) => {}

                            ChatEventInner::Message(msg) => {
                                let _ = backend.sender.send(proto::Event {
                                    inner: Some(EventInner::Message(proto::MessageEvent {
                                        source: msg
                                            .source
                                            .map(|source| id.source_to_relative(source)),
                                        text: msg.text,
                                    })),
                                });
                            }
                            ChatEventInner::PrivateMessage(private_msg) => {
                                let _ = backend.sender.send(proto::Event {
                                    inner: Some(EventInner::PrivateMessage(
                                        proto::PrivateMessageEvent {
                                            source: private_msg
                                                .source
                                                .map(|user| id.user_to_relative(user)),
                                            text: private_msg.text,
                                        },
                                    )),
                                });
                            }
                            ChatEventInner::Command(cmd_msg) => {
                                let _ = backend.sender.send(proto::Event {
                                    inner: Some(EventInner::Command(proto::CommandEvent {
                                        source: cmd_msg
                                            .source
                                            .map(|source| id.source_to_relative(source)),
                                        command: cmd_msg.command,
                                        arg: cmd_msg.arg,
                                    })),
                                });
                            }
                            ChatEventInner::Mention(mention_msg) => {
                                let _ = backend.sender.send(proto::Event {
                                    inner: Some(EventInner::Mention(proto::MentionEvent {
                                        source: mention_msg
                                            .source
                                            .map(|source| id.source_to_relative(source)),
                                        text: mention_msg.text,
                                    })),
                                });
                            }

                            ChatEventInner::JoinChannel(join) => {
                                let mut channels = backend_handle.channels.write().await;

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
                                let mut channels = backend_handle.channels.write().await;

                                channels.remove(&leave.channel_id);
                            }
                            ChatEventInner::ChangeChannel(change) => {
                                let mut channels = backend_handle.channels.write().await;

                                if let Some(channel) = channels.get_mut(&change.channel_id) {
                                    channel.display_name = change.display_name;
                                    channel.topic = change.topic;
                                }
                            }

                            // If a hello comes through, this client has broken
                            // the protocol contract, so we kill the connection.
                            ChatEventInner::Hello(_) => {
                                return Err(Status::new(
                                    Code::InvalidArgument,
                                    "unexpected chat event type",
                                ))
                            }
                        }
                    }
                    Either::Left((None, _)) => {
                        return Err(Status::new(Code::Internal, "internal request stream ended"))
                    }
                    Either::Right((None, _)) => {
                        return Err(Status::new(Code::Internal, "chat event stream ended"))
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
                    .map_err(|_| Status::new(Code::Internal, "failed to read internal event"))?;

                sender
                    .send(Ok(event))
                    .await
                    .map_err(|_| Status::new(Code::Internal, "failed to send event"))?;
            }
        });

        Ok(Response::new(receiver))
    }

    async fn send_message(
        &self,
        req: Request<proto::SendMessageRequest>,
    ) -> RpcResult<Response<proto::SendMessageResponse>> {
        let req = req.into_inner();
        let (backend_id, channel_id) = req
            .channel_id
            .parse::<FullId>()
            .map_err(|_| Status::new(Code::InvalidArgument, "failed to parse channel_id"))?
            .split();

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
            ChatEventInner::Failed(failed) => Err(Status::new(Code::Unknown, failed.reason)),
            _ => Err(Status::new(Code::Internal, "unexpected chat event")),
        }
    }

    async fn send_private_message(
        &self,
        req: Request<proto::SendPrivateMessageRequest>,
    ) -> RpcResult<Response<proto::SendPrivateMessageResponse>> {
        let req = req.into_inner();
        let (backend_id, user_id) = req
            .user_id
            .parse::<FullId>()
            .map_err(|_| Status::new(Code::InvalidArgument, "failed to parse user_id"))?
            .split();

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
            ChatEventInner::Failed(failed) => Err(Status::new(Code::Unknown, failed.reason)),
            _ => Err(Status::new(Code::Internal, "unexpected chat event")),
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
            .map_err(|_| Status::new(Code::InvalidArgument, "failed to parse backend_id"))?;

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
            ChatEventInner::Failed(failed) => Err(Status::new(Code::Unknown, failed.reason)),
            _ => Err(Status::new(Code::Internal, "unexpected chat event")),
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
            .map_err(|_| Status::new(Code::InvalidArgument, "failed to parse channel_id"))?
            .split();

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
            ChatEventInner::Failed(failed) => Err(Status::new(Code::Unknown, failed.reason)),
            _ => Err(Status::new(Code::Internal, "unexpected chat event")),
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
            .map_err(|_| Status::new(Code::InvalidArgument, "failed to parse channel_id"))?
            .split();

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
            ChatEventInner::Failed(failed) => Err(Status::new(Code::Unknown, failed.reason)),
            _ => Err(Status::new(Code::Internal, "unexpected chat event")),
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
            .map_err(|_| Status::new(Code::InvalidArgument, "failed to parse backend_id"))?;

        let backends = self.backends.read().await;
        let _backend = backends
            .get(&backend_id)
            .ok_or_else(|| Status::new(Code::NotFound, "backend not found"))?;

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
            .map_err(|_| Status::new(Code::InvalidArgument, "failed to parse backend_id"))?;

        let backends = self.backends.read().await;
        let channels = backends
            .get(&backend_id)
            .ok_or_else(|| Status::new(Code::NotFound, "backend not found"))?
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
            .map_err(|_| Status::new(Code::InvalidArgument, "failed to parse channel_id"))?
            .split();

        let backends = self.backends.read().await;
        let channels = backends
            .get(&backend_id)
            .ok_or_else(|| Status::new(Code::NotFound, "backend not found"))?
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
                .ok_or_else(|| Status::new(Code::NotFound, "channel not found"))?,
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
