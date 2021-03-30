use futures::future::{select, Either};
use futures::StreamExt;
use tokio::sync::mpsc;
use tonic::{Request, Response};

use crate::prelude::*;

use proto::{seabird::chat_ingest_server::ChatIngest, ChatEventInner, EventInner};

pub struct IngestEvents {
    id: BackendId,
    backend: Arc<super::ChatBackend>,
    backend_handle: super::ChatBackendHandle,
    server: Arc<super::Server>,
    input_stream: tonic::Streaming<proto::ChatEvent>,
    request_sender: mpsc::Sender<RpcResult<proto::ChatRequest>>,
}

impl IngestEvents {
    async fn new(
        id: BackendId,
        server: Arc<super::Server>,
        input_stream: tonic::Streaming<proto::ChatEvent>,
        request_sender: mpsc::Sender<RpcResult<proto::ChatRequest>>,
    ) -> RpcResult<Self> {
        let (backend_handle, backend) = server.register_backend(&id).await?;

        Ok(IngestEvents {
            id,
            backend,
            backend_handle,
            server,
            input_stream,
            request_sender,
        })
    }

    async fn handle_event(&mut self, event: proto::ChatEvent) -> RpcResult<()> {
        debug!("got event: {:?}", event);

        let inner = event
            .inner
            .ok_or_else(|| Status::internal("missing inner event"))?;

        if !event.id.is_empty() {
            self.server.respond(&event.id, inner.clone()).await;
        }

        match inner {
            // These are only used for responding to
            // requests, so we ignore them here because they
            // don't need to be handled specially.
            ChatEventInner::Success(_) => {}
            ChatEventInner::Failed(_) => {}
            ChatEventInner::Metadata(_) => {}

            ChatEventInner::Action(action) => {
                let _ = self.backend_handle.sender.send(proto::Event {
                    inner: Some(EventInner::Action(proto::ActionEvent {
                        source: action.source.map(|source| source.into_relative(&self.id)),
                        text: action.text,
                    })),
                    tags: event.tags,
                });
            }
            ChatEventInner::PrivateAction(private_action) => {
                let _ = self.backend_handle.sender.send(proto::Event {
                    inner: Some(EventInner::PrivateAction(proto::PrivateActionEvent {
                        source: private_action
                            .source
                            .map(|source| source.into_relative(&self.id)),
                        text: private_action.text,
                    })),
                    tags: event.tags,
                });
            }
            ChatEventInner::Message(msg) => {
                let _ = self.backend_handle.sender.send(proto::Event {
                    inner: Some(EventInner::Message(proto::MessageEvent {
                        source: msg.source.map(|source| source.into_relative(&self.id)),
                        text: msg.text,
                    })),
                    tags: event.tags,
                });
            }
            ChatEventInner::PrivateMessage(private_msg) => {
                let _ = self.backend_handle.sender.send(proto::Event {
                    inner: Some(EventInner::PrivateMessage(proto::PrivateMessageEvent {
                        source: private_msg.source.map(|user| user.into_relative(&self.id)),
                        text: private_msg.text,
                    })),
                    tags: event.tags,
                });
            }
            ChatEventInner::Command(cmd_msg) => {
                let _ = self.backend_handle.sender.send(proto::Event {
                    inner: Some(EventInner::Command(proto::CommandEvent {
                        source: cmd_msg.source.map(|source| source.into_relative(&self.id)),
                        command: cmd_msg.command,
                        arg: cmd_msg.arg,
                    })),
                    tags: event.tags,
                });
            }
            ChatEventInner::Mention(mention_msg) => {
                let _ = self.backend_handle.sender.send(proto::Event {
                    inner: Some(EventInner::Mention(proto::MentionEvent {
                        source: mention_msg
                            .source
                            .map(|source| source.into_relative(&self.id)),
                        text: mention_msg.text,
                    })),
                    tags: event.tags,
                });
            }

            ChatEventInner::JoinChannel(join) => {
                let mut channels = self.backend.channels.write().await;

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
                let mut channels = self.backend.channels.write().await;

                channels.remove(&leave.channel_id);
            }
            ChatEventInner::ChangeChannel(change) => {
                let mut channels = self.backend.channels.write().await;

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

        return Ok(());
    }

    async fn run(mut self) -> RpcResult<()> {
        loop {
            match select(
                self.backend_handle.receiver.next(),
                self.input_stream.next(),
            )
            .await
            {
                Either::Left((Some(req), _)) => {
                    debug!("got request: {:?}", req);

                    self.request_sender.send(Ok(req)).await.map_err(|err| {
                        Status::internal(format!("failed to send request to backend: {:?}", err))
                    })?;
                }
                Either::Right((Some(event), _)) => {
                    let event = event?;
                    self.handle_event(event).await?;
                }
                Either::Left((None, _)) => {
                    return Err(Status::internal("internal request stream ended"))
                }
                Either::Right((None, _)) => {
                    return Err(Status::internal("chat event stream ended"))
                }
            };
        }
    }
}

pub struct IngestEventsHandle {
    receiver: mpsc::Receiver<RpcResult<proto::ChatRequest>>,
}

impl IngestEventsHandle {
    pub fn new(
        server: Arc<super::Server>,
        mut input_stream: tonic::Streaming<proto::ChatEvent>,
    ) -> IngestEventsHandle {
        let (sender, receiver) = mpsc::channel(super::CHAT_INGEST_SEND_BUFFER);

        crate::spawn(async move {
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

            let ingest_events_actor = IngestEvents::new(id, server, input_stream, sender).await?;
            ingest_events_actor.run().await
        });

        IngestEventsHandle { receiver }
    }
}

impl futures::Stream for IngestEventsHandle {
    type Item = RpcResult<proto::ChatRequest>;

    fn poll_next(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Option<Self::Item>> {
        self.receiver.poll_recv(cx)
    }
}

#[async_trait]
impl ChatIngest for Arc<super::Server> {
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
