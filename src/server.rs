use std::pin::Pin;

use futures::stream::{Stream, StreamExt};
use futures::task::Poll;
use tokio::sync::{broadcast, mpsc, Mutex, RwLock};
use tokio::time::{delay_queue::Key as DelayQueueKey, DelayQueue, Duration, Instant};
use tonic::{Code, Request, Response, Status};
use uuid::Uuid;

use crate::prelude::*;
use crate::{
    proto,
    proto::seabird_server::{Seabird, SeabirdServer},
};

#[derive(Debug)]
pub enum CleanupEvent {
    StreamEnd { session_id: Uuid, stream_id: Uuid },
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
    sender: Arc<broadcast::Sender<proto::Event>>,
    cleanup_sender: mpsc::UnboundedSender<CleanupEvent>,
    cleanup_receiver: Mutex<mpsc::UnboundedReceiver<CleanupEvent>>,
    message_sender: mpsc::UnboundedSender<irc::Message>,
    message_receiver: Mutex<mpsc::UnboundedReceiver<irc::Message>>,
    tracker: RwLock<crate::irc::Tracker>,
    sessions: RwLock<BTreeMap<Uuid, Arc<RwLock<SessionMetadata>>>>,
}

#[derive(Debug)]
struct SessionMetadata {
    session_id: Uuid,
    session_start: Instant,
    session_tag: String,
    commands: HashMap<String, proto::CommandMetadata>,
    streams: BTreeSet<Uuid>,

    cleanup_key: Option<DelayQueueKey>,
    cleanup_sender: mpsc::UnboundedSender<CleanupEvent>,
}

impl SessionMetadata {
    fn new(
        tag: String,
        commands: HashMap<String, proto::CommandMetadata>,
        cleanup_sender: mpsc::UnboundedSender<CleanupEvent>,
    ) -> Self {
        SessionMetadata {
            session_id: Uuid::new_v4(),
            session_start: Instant::now(),
            session_tag: tag,
            streams: BTreeSet::new(),
            commands,
            cleanup_key: None,
            cleanup_sender,
        }
    }

    fn get_stream_handle(&mut self) -> StreamHandle {
        let handle = StreamHandle::new(self.session_id, self.cleanup_sender.clone());
        self.streams.insert(handle.stream_id);
        handle
    }
}
#[derive(Debug)]
struct StreamHandle {
    session_id: Uuid,
    stream_id: Uuid,
    cleanup_sender: mpsc::UnboundedSender<CleanupEvent>,
}

impl StreamHandle {
    fn new(session_id: Uuid, cleanup_sender: mpsc::UnboundedSender<CleanupEvent>) -> Self {
        StreamHandle {
            session_id,
            stream_id: Uuid::new_v4(),
            cleanup_sender,
        }
    }
}

impl Drop for StreamHandle {
    fn drop(&mut self) {
        self.cleanup_sender
            .send(CleanupEvent::StreamEnd {
                session_id: self.session_id,
                stream_id: self.stream_id,
            })
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
        let (sender, _) = broadcast::channel(16);
        let (cleanup_sender, cleanup_receiver) = mpsc::unbounded_channel();
        let (message_sender, message_receiver) = mpsc::unbounded_channel();

        Server {
            config,
            sender: Arc::new(sender),
            cleanup_sender,
            cleanup_receiver: Mutex::new(cleanup_receiver),
            message_sender,
            message_receiver: Mutex::new(message_receiver),
            tracker: RwLock::new(crate::irc::Tracker::new()),
            sessions: RwLock::new(BTreeMap::new()),
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
            service.run_cleanup(),
        )
        .await?;

        Ok(())
    }
}

impl Server {
    async fn handle_cleanup_queue(self: &Arc<Self>, session_id: Uuid) {
        let mut sessions_guard = self.sessions.write().await;
        let remove = if let Some(session) = sessions_guard.get(&session_id) {
            let session_guard = session.write().await;

            // If there are still no running streams on this session, kill it
            // with fire.
            session_guard.streams.is_empty()
        } else {
            false
        };

        if remove {
            trace!("removing session: {}", session_id);
            sessions_guard.remove(&session_id);
        }
    }

    async fn handle_cleanup_event(
        self: &Arc<Self>,
        cleanup_queue: &mut DelayQueue<Uuid>,
        event: CleanupEvent,
    ) {
        match event {
            CleanupEvent::StreamEnd {
                session_id,
                stream_id,
            } => {
                let sessions_guard = self.sessions.read().await;
                if let Some(session) = sessions_guard.get(&session_id) {
                    let mut session_guard = session.write().await;

                    trace!("removing stream: {}", stream_id);
                    if !session_guard.streams.remove(&stream_id) {
                        warn!("A stream was marked dead but did not exist. There is most likely a leak somewhere.");
                    }

                    let key = session_guard.cleanup_key.take();

                    if let Some(key) = key {
                        // If they've recovered, remove this key from the delay
                        // queue. Otherwise, put it back inside the session,
                        // adding to the delay.
                        if session_guard.streams.is_empty() {
                            session_guard.cleanup_key.replace(key);
                        } else {
                            cleanup_queue.remove(&key);
                        }
                    } else {
                        // If there was not already a key and there are no
                        // running streams on this session, queue up a removal.
                        if session_guard.streams.is_empty() {
                            let key = cleanup_queue.insert(session_id, Duration::from_secs(5));
                            session_guard.cleanup_key.replace(key);
                        }
                    }
                }
            }
        }
    }

    async fn run_cleanup(self: &Arc<Self>) -> Result<()> {
        use futures::future::{select, Either};

        let mut receiver = self.cleanup_receiver.try_lock()?;
        let mut cleanup_queue: DelayQueue<Uuid> = DelayQueue::new();

        loop {
            // We select on
            match select(cleanup_queue.next(), receiver.next()).await {
                // Deadline on the cleanup queue
                Either::Left((Some(Ok(session_id)), _)) => {
                    self.handle_cleanup_queue(session_id.into_inner()).await;
                }
                Either::Left((Some(Err(_)), _)) => anyhow::bail!("cleanup queue unexpected error"),

                // The only thing that can insert into the cleanup queue is the
                // cleanup event, so if we got none, just wait for a cleanup
                // event.
                Either::Left((None, next_cleanup_event)) => {
                    self.handle_cleanup_event(
                        &mut cleanup_queue,
                        next_cleanup_event
                            .await
                            .ok_or_else(|| anyhow!("cleanup event receiver closed early"))?,
                    )
                    .await
                }

                // Incoming cleanup event
                Either::Right((Some(event), _)) => {
                    self.handle_cleanup_event(&mut cleanup_queue, event).await;
                }
                Either::Right((None, _)) => anyhow::bail!("cleanup event receiver closed early"),
            };
        }
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

    async fn validate_internal(
        &self,
        identity: Option<&proto::Identity>,
        stream: bool,
    ) -> RpcResult<Option<StreamHandle>> {
        use proto::identity::AuthMethod;

        let identity =
            identity.ok_or_else(|| Status::new(Code::Unauthenticated, "missing identity"))?;

        match identity
            .auth_method
            .as_ref()
            .ok_or_else(|| Status::new(Code::Unauthenticated, "missing auth_method"))?
        {
            AuthMethod::Token(auth_token) => {
                let auth_token = auth_token
                    .parse()
                    .map_err(|_| Status::new(Code::Unauthenticated, "invalid token"))?;

                let sessions_guard = self.sessions.read().await;

                let mut session_guard = sessions_guard
                    .get(&auth_token)
                    .ok_or_else(|| Status::new(Code::Unauthenticated, "session does not exist"))?
                    .write()
                    .await;

                if stream {
                    Ok(Some(session_guard.get_stream_handle()))
                } else {
                    Ok(None)
                }
            }
        }
    }

    async fn validate_identity(&self, identity: Option<&proto::Identity>) -> RpcResult<()> {
        // We this shouldn't generate a stream handle, but we drop it just in
        // case.
        self.validate_internal(identity, false).await?;
        Ok(())
    }

    async fn validate_stream(&self, identity: Option<&proto::Identity>) -> RpcResult<StreamHandle> {
        Ok(self
            .validate_internal(identity, true)
            .await?
            .ok_or_else(|| Status::new(Code::Internal, "failed to get session handle"))?)
    }
}

#[async_trait]
impl Seabird for Arc<Server> {
    type EventsStream = EventStreamReceiver;

    async fn open_session(
        &self,
        request: Request<proto::OpenSessionRequest>,
    ) -> RpcResult<Response<proto::OpenSessionResponse>> {
        use proto::identity::AuthMethod;

        let request = request.into_inner();

        let mut guard = self.sessions.write().await;

        let meta = SessionMetadata::new(request.tag, request.commands, self.cleanup_sender.clone());

        if let Some(_) = guard.get(&meta.session_id) {
            return Err(Status::new(Code::Internal, "duplicate session_id"));
        }

        let response = Response::new(proto::OpenSessionResponse {
            identity: Some(proto::Identity {
                auth_method: Some(AuthMethod::Token(meta.session_id.to_string())),
            }),
        });

        guard.insert(meta.session_id.clone(), Arc::new(RwLock::new(meta)));

        // TODO: handle command registration

        Ok(response)
    }

    async fn events(
        &self,
        request: Request<proto::EventsRequest>,
    ) -> RpcResult<Response<Self::EventsStream>> {
        let request = request.into_inner();
        let stream_handle = self.validate_stream(request.identity.as_ref()).await?;

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
        self.validate_identity(request.identity.as_ref()).await?;

        self.message_sender
            .send(irc::Message::new(
                "PRIVMSG".to_string(),
                vec![request.target, request.message],
            ))
            .map_err(|_| Status::new(Code::Internal, "failed to send message"))?;

        Ok(Response::new(proto::SendMessageResponse {}))
    }

    async fn send_raw_message(
        &self,
        request: Request<proto::SendRawMessageRequest>,
    ) -> RpcResult<Response<proto::SendRawMessageResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref()).await?;

        self.message_sender
            .send(irc::Message::new(request.command, request.params))
            .map_err(|_| Status::new(Code::Internal, "failed to send message"))?;

        Ok(Response::new(proto::SendRawMessageResponse {}))
    }

    async fn list_channels(
        &self,
        request: Request<proto::ListChannelsRequest>,
    ) -> RpcResult<Response<proto::ListChannelsResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref()).await?;

        Ok(Response::new(proto::ListChannelsResponse {
            names: self.tracker.read().await.list_channels(),
        }))
    }

    async fn get_channel_info(
        &self,
        request: Request<proto::ChannelInfoRequest>,
    ) -> RpcResult<Response<proto::ChannelInfoResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref()).await?;

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

    async fn set_channel_info(
        &self,
        request: Request<proto::SetChannelInfoRequest>,
    ) -> RpcResult<Response<proto::SetChannelInfoResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref()).await?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn join_channel(
        &self,
        request: Request<proto::JoinChannelRequest>,
    ) -> RpcResult<Response<proto::JoinChannelResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref()).await?;

        // TODO: this could arguably wait for the channel to be synced.

        self.message_sender
            .send(irc::Message::new("JOIN".to_string(), vec![request.target]))
            .map_err(|_| Status::new(Code::Internal, "failed to join channel"))?;

        Ok(Response::new(proto::JoinChannelResponse {}))
    }

    async fn leave_channel(
        &self,
        request: Request<proto::LeaveChannelRequest>,
    ) -> RpcResult<Response<proto::LeaveChannelResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref()).await?;

        self.message_sender
            .send(irc::Message::new(
                "PART".to_string(),
                vec![request.target, request.message],
            ))
            .map_err(|_| Status::new(Code::Internal, "failed to leave channel"))?;

        Ok(Response::new(proto::LeaveChannelResponse {}))
    }

    async fn list_sessions(
        &self,
        request: Request<proto::ListSessionsRequest>,
    ) -> RpcResult<Response<proto::ListSessionsResponse>> {
        trace!("call to list_sessions");
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref()).await?;

        let session_ids = self
            .sessions
            .read()
            .await
            .keys()
            .into_iter()
            .map(|uuid| uuid.to_string())
            .collect();

        Ok(Response::new(proto::ListSessionsResponse { session_ids }))
    }

    async fn get_session_info(
        &self,
        request: Request<proto::SessionInfoRequest>,
    ) -> RpcResult<Response<proto::SessionInfoResponse>> {
        trace!("call to get_session_info");
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref()).await?;

        let session_id = request
            .session_id
            .parse()
            .map_err(|_| Status::new(Code::Unauthenticated, "invalid session_id"))?;

        let (session_tag, stream_ids, commands) = {
            let sessions_guard = self.sessions.read().await;
            match sessions_guard.get(&session_id) {
                Some(session) => {
                    let session_guard = session.read().await;
                    Ok((
                        session_guard.session_tag.clone(),
                        session_guard
                            .streams
                            .iter()
                            .map(|uuid| uuid.to_string())
                            .collect(),
                        session_guard.commands.clone(),
                    ))
                }
                None => Err(Status::new(Code::NotFound, "session not found")),
            }
        }?;

        Ok(Response::new(proto::SessionInfoResponse {
            session_tag,
            session_id: session_id.to_string(),
            stream_ids,
            commands,
        }))
    }
}
