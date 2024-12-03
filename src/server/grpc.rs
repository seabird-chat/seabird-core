use std::time::{SystemTime, UNIX_EPOCH};

use futures::future::{select, Either};
use futures::StreamExt;
use tokio_stream::wrappers::BroadcastStream;
use tonic::{Request, Response};

use crate::prelude::*;

use proto::{seabird::seabird_server::Seabird, ChatEventInner, EventInner};

use super::auth::extract_auth_username;

#[async_trait]
impl Seabird for Arc<super::Server> {
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
            crate::wrapped::channel(super::CHAT_INGEST_RECEIVE_BUFFER);

        let mut events = BroadcastStream::new(self.sender.subscribe());

        let cleanup_sender = self.cleanup_sender.clone();

        // Spawn a task to handle the stream
        crate::spawn(async move {
            let _handle = super::CommandsHandle {
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

        let (text, blocks) = normalize_message(&req.text, req.blocks)?;

        self.broadcast(
            EventInner::SendMessage(proto::SendMessageEvent {
                sender: username.to_string(),
                channel_id: req.channel_id,
                text: text.clone(),
                blocks: blocks.clone(),
            }),
            req.tags.clone(),
        );

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::SendMessage(proto::SendMessageChatRequest {
                    channel_id,
                    text,
                    blocks,
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

        let (text, blocks) = normalize_message(&req.text, req.blocks)?;

        self.broadcast(
            EventInner::SendPrivateMessage(proto::SendPrivateMessageEvent {
                sender: username.to_string(),
                user_id: req.user_id,
                text: text.clone(),
                blocks: blocks.clone(),
            }),
            req.tags.clone(),
        );

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::SendPrivateMessage(proto::SendPrivateMessageChatRequest {
                    user_id,
                    text,
                    blocks,
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

        let (text, blocks) = normalize_message(&req.text, req.blocks)?;

        self.broadcast(
            EventInner::PerformAction(proto::PerformActionEvent {
                sender: username.to_string(),
                channel_id: req.channel_id,
                text: text.clone(),
                blocks: blocks.clone(),
            }),
            req.tags.clone(),
        );

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::PerformAction(proto::PerformActionChatRequest {
                    channel_id,
                    text,
                    blocks,
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

        let (text, blocks) = normalize_message(&req.text, req.blocks)?;

        self.broadcast(
            EventInner::PerformPrivateAction(proto::PerformPrivateActionEvent {
                sender: username.to_string(),
                user_id: req.user_id,
                text: text.clone(),
                blocks: blocks.clone(),
            }),
            req.tags.clone(),
        );

        let resp = self
            .issue_request(
                backend_id,
                proto::ChatRequestInner::PerformPrivateAction(
                    proto::PerformPrivateActionChatRequest {
                        user_id,
                        text,
                        blocks,
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
