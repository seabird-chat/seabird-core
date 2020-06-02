use std::net::SocketAddr;
use std::pin::Pin;

use futures::stream::{Stream, StreamExt};
use futures::task::Poll;
use time::OffsetDateTime;
use tokio::sync::{broadcast, mpsc, Mutex, RwLock};
use tonic::{Code, Request, Response, Status};
use uuid::Uuid;

use crate::prelude::*;
use crate::{
    proto,
    proto::seabird_server::{Seabird, SeabirdServer},
};

const BROADCAST_BUFFER: usize = 32;

#[derive(Debug)]
pub enum InternalEvent {
    SetTokens { tokens: BTreeMap<String, String> },
    StreamEnd { stream_id: Uuid },
}

#[derive(Clone, Debug)]
pub struct ServerConfig {
    bind_host: String,
    irc_host: String,
    nick: String,
    user: Option<String>,
    name: Option<String>,
    pass: Option<String>,
    command_prefix: String,
}

impl ServerConfig {
    pub fn new(
        bind_host: String,
        irc_host: String,
        nick: String,
        user: Option<String>,
        name: Option<String>,
        pass: Option<String>,
        command_prefix: Option<String>,
    ) -> Self {
        ServerConfig {
            bind_host,
            irc_host,
            nick,
            user,
            name,
            pass,
            command_prefix: command_prefix.unwrap_or_else(|| "!".to_string()),
        }
    }
}

#[derive(Debug)]
pub struct Server {
    config: ServerConfig,

    // Sender allows us to broadcast to everyone. Call sender.subscribe() to get
    // a stream of events.
    sender: Arc<broadcast::Sender<proto::Event>>,

    // The internal sender acts as an event bus and allows for cleanup events to
    // be sent as well as config update events.
    internal_sender: mpsc::UnboundedSender<InternalEvent>,
    internal_receiver: Mutex<mpsc::UnboundedReceiver<InternalEvent>>,

    // Message sender allows anyone to send a message to the IRC server.
    message_sender: mpsc::UnboundedSender<irc::Message>,
    message_receiver: Mutex<mpsc::UnboundedReceiver<irc::Message>>,

    // Tracker tracks channel/user information
    tracker: RwLock<crate::irc::Tracker>,

    // Tokens is a mapping of token to tag
    tokens: RwLock<BTreeMap<String, String>>,

    start_time: OffsetDateTime,

    // Streams is a mapping of stream ID to stream metadata. This will be
    // cleaned up by the cleanup task.
    streams: RwLock<BTreeMap<Uuid, Arc<RwLock<StreamMetadata>>>>,
}

#[derive(Debug)]
struct StreamMetadata {
    id: Uuid,
    tag: String,
    start_time: OffsetDateTime,
    remote_addr: SocketAddr,
    commands: HashMap<String, proto::CommandMetadata>,
    handle: Option<StreamHandle>,
}

impl StreamMetadata {
    fn new(
        tag: String,
        remote_addr: SocketAddr,
        commands: HashMap<String, proto::CommandMetadata>,
        internal_sender: mpsc::UnboundedSender<InternalEvent>,
    ) -> Self {
        let id = Uuid::new_v4();
        StreamMetadata {
            id: id.clone(),
            tag,
            start_time: OffsetDateTime::now_utc(),
            remote_addr,
            commands,
            handle: Some(StreamHandle::new(id, internal_sender)),
        }
    }

    fn take_handle(&mut self) -> Option<StreamHandle> {
        self.handle.take()
    }
}
#[derive(Debug)]
struct StreamHandle {
    id: Uuid,
    internal_sender: mpsc::UnboundedSender<InternalEvent>,
}

impl StreamHandle {
    fn new(stream_id: Uuid, internal_sender: mpsc::UnboundedSender<InternalEvent>) -> Self {
        let ret = StreamHandle {
            id: stream_id,
            internal_sender: internal_sender,
        };
        ret
    }
}

impl Drop for StreamHandle {
    fn drop(&mut self) {
        self.internal_sender
            .send(InternalEvent::StreamEnd { stream_id: self.id })
            .unwrap();
    }
}

#[derive(Debug)]
pub struct EventStreamReceiver {
    stream_handle: StreamHandle,
    receiver: broadcast::Receiver<proto::Event>,
}

impl EventStreamReceiver {
    fn new(handle: StreamHandle, receiver: broadcast::Receiver<proto::Event>) -> Self {
        EventStreamReceiver {
            stream_handle: handle,
            receiver,
        }
    }
}

impl Stream for EventStreamReceiver {
    type Item = RpcResult<proto::Event>;

    fn poll_next(
        mut self: Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> Poll<Option<Self::Item>> {
        self.receiver.poll_recv(cx).map(|v| match v {
            Ok(val) => Some(Ok(val)),

            // If we get an error, we want to end the stream - if something is
            // lagged, we're done with it.
            Err(_) => None,
        })
    }
}

impl Server {
    pub fn new(config: ServerConfig) -> Self {
        // We actually don't care about the receiving side - the clients will
        // subscribe to it later.
        let (sender, _) = broadcast::channel(BROADCAST_BUFFER);
        let (internal_sender, internal_receiver) = mpsc::unbounded_channel();
        let (message_sender, message_receiver) = mpsc::unbounded_channel();

        Server {
            config,
            tokens: RwLock::new(BTreeMap::new()),
            sender: Arc::new(sender),
            internal_sender,
            internal_receiver: Mutex::new(internal_receiver),
            message_sender,
            message_receiver: Mutex::new(message_receiver),
            tracker: RwLock::new(crate::irc::Tracker::new()),
            streams: RwLock::new(BTreeMap::new()),
            start_time: OffsetDateTime::now_utc(),
        }
    }

    pub async fn run(self) -> Result<()> {
        // Because the gRPC server takes ownership, the easiest way to deal with
        // this is to move the Server into an Arc where multiple references can
        // exist at once.
        let service = Arc::new(self);

        info!("running server");

        futures::future::try_join3(
            service.run_irc_client(),
            service.run_grpc_server(),
            service.run_internal_event_loop(),
        )
        .await?;

        Ok(())
    }

    pub fn get_internal_sender(&self) -> mpsc::UnboundedSender<InternalEvent> {
        return self.internal_sender.clone();
    }
}

impl Server {
    async fn run_internal_event_loop(self: &Arc<Self>) -> Result<()> {
        let mut receiver = self.internal_receiver.try_lock()?;

        while let Some(event) = receiver.next().await {
            match event {
                InternalEvent::SetTokens { tokens } => {
                    let mut tokens_guard = self.tokens.write().await;
                    debug!("updating tokens");
                    *tokens_guard = tokens;
                }
                InternalEvent::StreamEnd { stream_id } => {
                    let mut streams_guard = self.streams.write().await;

                    if streams_guard.remove(&stream_id).is_none() {
                        warn!(
                            "attempted to remove stream {} but it did not exist",
                            stream_id
                        );
                    } else {
                        info!("removed stream: {}", stream_id);
                    }
                }
            }
        }

        anyhow::bail!("run_internal_event_loop exited early");
    }

    async fn run_irc_client(self: &Arc<Self>) -> Result<()> {
        let config = self.config.clone();

        let irc_client = crate::irc::Client::new(
            config.irc_host.clone(),
            config.nick.clone(),
            config.user.clone(),
            config.name.clone(),
            config.pass.clone(),
        )
        .await?;

        futures::future::try_join(
            self.run_irc_client_reader(&irc_client, &config),
            self.run_irc_client_writer(&irc_client),
        )
        .await?;

        anyhow::bail!("run_irc_client exited early");
    }

    async fn run_irc_client_reader(
        self: &Arc<Self>,
        irc_client: &crate::irc::Client,
        config: &ServerConfig,
    ) -> Result<()> {
        use proto::event::Inner;

        let command_prefix = config.command_prefix.as_str();

        loop {
            let msg = irc_client.recv().await?;

            self.tracker.write().await.handle_message(&msg);

            match (msg.command.as_str(), msg.params.len(), msg.prefix.as_ref()) {
                ("001", _, _) => {
                    irc_client.write(format!("JOIN #encoded-test")).await?;
                }
                ("PRIVMSG", 2, Some(prefix)) => {
                    let inner = {
                        let current_nick = irc_client.get_current_nick().await;
                        let sender = prefix.nick.clone();
                        let target = msg.params[0].clone();
                        let message = msg.params[1].clone();

                        if target == current_nick {
                            // Private message
                            Inner::PrivateMessage(proto::PrivateMessageEvent {
                                reply_to: sender.clone(),
                                sender,
                                message,
                            })
                        } else if message.starts_with(command_prefix) {
                            // Command event
                            let mut parts = message[command_prefix.len()..].splitn(2, ' ');
                            Inner::Command(proto::CommandEvent {
                                reply_to: target,
                                sender,
                                command: parts.next().unwrap().to_string(),
                                arg: String::from(parts.next().map(|s| s.trim()).unwrap_or("")),
                            })
                        } else if message.len() >= current_nick.len() + 1
                            && message.starts_with(&current_nick)
                            && !char::from(message.as_bytes()[current_nick.len()]).is_alphanumeric()
                        {
                            // Mention event
                            let message = &message[current_nick.len() + 1..];
                            Inner::Mention(proto::MentionEvent {
                                reply_to: target,
                                sender,
                                message: message.to_string(),
                            })
                        } else {
                            Inner::Message(proto::MessageEvent {
                                reply_to: target,
                                sender,
                                message,
                            })
                        }
                    };

                    // TODO: handle help command

                    // We actually want to ignore this error as it will only
                    // happen if there are no receivers, which is a valid state.
                    let _ = self.sender.send(proto::Event { inner: Some(inner) });
                }
                _ => {}
            }
        }
    }

    async fn run_irc_client_writer(
        self: &Arc<Self>,
        irc_client: &crate::irc::Client,
    ) -> Result<()> {
        let mut receiver = self.message_receiver.try_lock()?;

        while let Some(msg) = receiver.next().await {
            irc_client.write_message(&msg).await?;
        }

        anyhow::bail!("run_irc_client_writer exited early");
    }

    async fn run_grpc_server(self: &Arc<Self>) -> Result<()> {
        let addr = self.config.bind_host.parse()?;

        tonic::transport::Server::builder()
            .add_service(SeabirdServer::new(self.clone()))
            .serve(addr)
            .await?;

        anyhow::bail!("run_grpc_server exited early");
    }

    async fn validate_identity(
        &self,
        method: &str,
        identity: Option<&proto::Identity>,
    ) -> RpcResult<String> {
        use proto::identity::AuthMethod;

        trace!("call to {}", method);

        let identity =
            identity.ok_or_else(|| Status::new(Code::Unauthenticated, "missing identity"))?;

        let tag = match identity
            .auth_method
            .as_ref()
            .ok_or_else(|| Status::new(Code::Unauthenticated, "missing auth_method"))?
        {
            AuthMethod::Token(auth_token) => self
                .tokens
                .read()
                .await
                .get(auth_token)
                .map(|s| s.to_string())
                .ok_or_else(|| Status::new(Code::Unauthenticated, "unknown token"))?,
        };

        debug!("authenticated call to {} rpc with tag {}", method, tag);

        Ok(tag)
    }

    async fn new_stream(
        &self,
        tag: String,
        remote_addr: SocketAddr,
        commands: HashMap<String, proto::CommandMetadata>,
    ) -> RpcResult<StreamHandle> {
        // Build the stream metadata
        let mut meta = StreamMetadata::new(
            tag.clone(),
            remote_addr,
            commands,
            self.internal_sender.clone(),
        );
        debug!("new stream {} with tag {}", meta.id, &tag);

        let handle = meta.take_handle().ok_or_else(|| {
            Status::new(
                Code::Internal,
                format!("failed to create new stream handle for {}", &tag),
            )
        });

        // Add the metadata to the tracked streams
        self.streams
            .write()
            .await
            .insert(meta.id.clone(), Arc::new(RwLock::new(meta)));

        handle
    }
}

#[async_trait]
impl Seabird for Arc<Server> {
    type StreamEventsStream = EventStreamReceiver;

    async fn stream_events(
        &self,
        request: Request<proto::StreamEventsRequest>,
    ) -> RpcResult<Response<Self::StreamEventsStream>> {
        let remote_addr = request
            .remote_addr()
            .ok_or_else(|| Status::new(Code::Internal, "missing remote_addr"))?;
        let request = request.into_inner();
        let tag = self
            .validate_identity("stream_events", request.identity.as_ref())
            .await?;

        let stream_handle = self.new_stream(tag, remote_addr, request.commands).await?;

        Ok(Response::new(EventStreamReceiver::new(
            stream_handle,
            self.sender.subscribe(),
        )))
    }

    async fn send_message(
        &self,
        request: Request<proto::SendMessageRequest>,
    ) -> RpcResult<Response<proto::SendMessageResponse>> {
        let request = request.into_inner();
        self.validate_identity("send_message", request.identity.as_ref())
            .await?;

        self.message_sender
            .send(irc::Message::new(
                "PRIVMSG".to_string(),
                vec![request.target, request.message],
            ))
            .map_err(|_| Status::new(Code::Internal, "failed to send message"))?;

        Ok(Response::new(proto::SendMessageResponse {}))
    }

    async fn list_channels(
        &self,
        request: Request<proto::ListChannelsRequest>,
    ) -> RpcResult<Response<proto::ListChannelsResponse>> {
        let request = request.into_inner();
        self.validate_identity("list_channels", request.identity.as_ref())
            .await?;

        Ok(Response::new(proto::ListChannelsResponse {
            names: self.tracker.read().await.list_channels(),
        }))
    }

    async fn get_channel_info(
        &self,
        request: Request<proto::ChannelInfoRequest>,
    ) -> RpcResult<Response<proto::ChannelInfoResponse>> {
        let request = request.into_inner();
        self.validate_identity("get_channel_info", request.identity.as_ref())
            .await?;

        match self.tracker.read().await.get_channel(request.name.as_str()) {
            Some(channel) => Ok(Response::new(proto::ChannelInfoResponse {
                name: channel.name,
                topic: channel.topic.unwrap_or_else(|| "".to_string()),
                users: channel
                    .users
                    .into_iter()
                    .map(|nick| proto::User { nick })
                    .collect(),
            })),
            None => Err(Status::new(Code::NotFound, "channel not found")),
        }
    }

    async fn set_channel_topic(
        &self,
        request: Request<proto::SetChannelTopicRequest>,
    ) -> RpcResult<Response<proto::SetChannelTopicResponse>> {
        let request = request.into_inner();
        self.validate_identity("set_channel_topic", request.identity.as_ref())
            .await?;

        self.message_sender
            .send(irc::Message::new(
                "TOPIC".to_string(),
                vec![request.name, request.topic],
            ))
            .map_err(|_| Status::new(Code::Internal, "failed set channel topic"))?;

        Ok(Response::new(proto::SetChannelTopicResponse {}))
    }

    async fn join_channel(
        &self,
        request: Request<proto::JoinChannelRequest>,
    ) -> RpcResult<Response<proto::JoinChannelResponse>> {
        let request = request.into_inner();
        self.validate_identity("join_channel", request.identity.as_ref())
            .await?;

        // TODO: this could arguably wait for the channel to be synced.

        self.message_sender
            .send(irc::Message::new("JOIN".to_string(), vec![request.name]))
            .map_err(|_| Status::new(Code::Internal, "failed to join channel"))?;

        Ok(Response::new(proto::JoinChannelResponse {}))
    }

    async fn leave_channel(
        &self,
        request: Request<proto::LeaveChannelRequest>,
    ) -> RpcResult<Response<proto::LeaveChannelResponse>> {
        let request = request.into_inner();
        self.validate_identity("leave_channel", request.identity.as_ref())
            .await?;

        self.message_sender
            .send(irc::Message::new(
                "PART".to_string(),
                vec![request.name, request.message],
            ))
            .map_err(|_| Status::new(Code::Internal, "failed to leave channel"))?;

        Ok(Response::new(proto::LeaveChannelResponse {}))
    }

    async fn list_streams(
        &self,
        request: Request<proto::ListStreamsRequest>,
    ) -> RpcResult<Response<proto::ListStreamsResponse>> {
        let request = request.into_inner();
        self.validate_identity("list_streams", request.identity.as_ref())
            .await?;

        let stream_ids = self
            .streams
            .read()
            .await
            .keys()
            .into_iter()
            .map(|uuid| uuid.to_string())
            .collect();

        Ok(Response::new(proto::ListStreamsResponse { stream_ids }))
    }

    async fn get_stream_info(
        &self,
        request: Request<proto::StreamInfoRequest>,
    ) -> RpcResult<Response<proto::StreamInfoResponse>> {
        let request = request.into_inner();
        self.validate_identity("get_stream_info", request.identity.as_ref())
            .await?;

        let stream_id = request
            .stream_id
            .parse()
            .map_err(|_| Status::new(Code::InvalidArgument, format!("invalid stream_id")))?;

        let (stream_tag, stream_start, stream_addr, commands) = {
            let streams_guard = self.streams.read().await;
            match streams_guard.get(&stream_id) {
                Some(stream) => {
                    let stream_guard = stream.read().await;
                    Ok((
                        stream_guard.tag.clone(),
                        stream_guard.start_time.timestamp(),
                        stream_guard.remote_addr.to_string(),
                        stream_guard.commands.clone(),
                    ))
                }
                None => Err(Status::new(Code::NotFound, "stream not found")),
            }
        }?;

        Ok(Response::new(proto::StreamInfoResponse {
            id: request.stream_id,
            tag: stream_tag,
            commands,
            connection_timestamp: stream_start,
            remote_address: stream_addr,
        }))
    }

    async fn get_core_info(
        &self,
        request: Request<proto::CoreInfoRequest>,
    ) -> RpcResult<Response<proto::CoreInfoResponse>> {
        let request = request.into_inner();
        self.validate_identity("get_core_info", request.identity.as_ref())
            .await?;

        Ok(Response::new(proto::CoreInfoResponse {
            current_nick: self
                .tracker
                .read()
                .await
                .get_current_nick()
                .unwrap_or_else(|| "".to_string()),
            startup_timestamp: self.start_time.timestamp(),
        }))
    }
}
