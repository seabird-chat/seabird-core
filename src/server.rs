use std::pin::Pin;

use futures::Stream;
use tokio::sync::RwLock;
use tonic::{Code, Request, Response, Status};
use uuid::Uuid;

use crate::prelude::*;
use crate::{
    proto,
    proto::seabird_server::{Seabird, SeabirdServer},
};

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
    tracker: RwLock<crate::irc::Tracker>,
}

impl Server {
    pub fn new(config: ServerConfig) -> Self {
        Server {
            config,
            tracker: RwLock::new(crate::irc::Tracker::new()),
        }
    }

    pub async fn run(self) -> Result<()> {
        // Because the gRPC server takes ownership, the easiest way to deal with
        // this is to move the Server into an Arc where multiple references can
        // exist at once.
        let service = Arc::new(self);

        futures::future::try_join(service.run_irc_client(), service.run_grpc_server()).await?;

        Ok(())
    }
}

impl Server {
    async fn run_irc_client(self: &Arc<Self>) -> Result<()> {
        let config = self.config.clone();
        let irc_client = crate::irc::Client::new(
            config.irc_host,
            config.nick,
            config.user,
            config.name,
            config.pass,
        )
        .await?;

        loop {
            let msg = irc_client.recv().await?;

            self.tracker.write().await.handle_message(&msg);

            match msg.command.as_str() {
                "001" => {
                    irc_client.write(format!("JOIN #encoded-test")).await?;
                }
                "PRIVMSG" => println!("{:?}", msg),
                _ => {}
            }
        }
    }

    async fn run_grpc_server(self: &Arc<Self>) -> Result<()> {
        let addr = self.config.bind_host.parse()?;

        tonic::transport::Server::builder()
            .add_service(SeabirdServer::new(self.clone()))
            .serve(addr)
            .await?;

        anyhow::bail!("run_grpc_server exited early");
    }

    fn validate_identity(&self, identity: Option<&proto::Identity>) -> RpcResult<()> {
        let identity =
            identity.ok_or_else(|| Status::new(Code::Unauthenticated, "missing identity"))?;

        todo!();
    }
}

#[async_trait]
impl Seabird for Arc<Server> {
    type EventsStream =
        Pin<Box<dyn Stream<Item = std::result::Result<proto::Event, Status>> + Send + Sync>>;

    async fn register(
        &self,
        request: Request<proto::RegisterRequest>,
    ) -> RpcResult<Response<proto::RegisterResponse>> {
        let request = request.into_inner();

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn events(
        &self,
        request: Request<proto::EventsRequest>,
    ) -> RpcResult<Response<Self::EventsStream>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn send_message(
        &self,
        request: Request<proto::SendMessageRequest>,
    ) -> RpcResult<Response<proto::SendMessageResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn send_raw_message(
        &self,
        request: Request<proto::SendRawMessageRequest>,
    ) -> RpcResult<Response<proto::SendRawMessageResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn list_channels(
        &self,
        request: Request<proto::ListChannelsRequest>,
    ) -> RpcResult<Response<proto::ListChannelsResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn get_channel_info(
        &self,
        request: Request<proto::ChannelInfoRequest>,
    ) -> RpcResult<Response<proto::ChannelInfoResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn set_channel_info(
        &self,
        request: Request<proto::SetChannelInfoRequest>,
    ) -> RpcResult<Response<proto::SetChannelInfoResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn join_channel(
        &self,
        request: Request<proto::JoinChannelRequest>,
    ) -> RpcResult<Response<proto::JoinChannelResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn leave_channel(
        &self,
        request: Request<proto::LeaveChannelRequest>,
    ) -> RpcResult<Response<proto::LeaveChannelResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn list_plugins(
        &self,
        request: Request<proto::ListPluginsRequest>,
    ) -> RpcResult<Response<proto::ListPluginsResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }

    async fn get_plugin_info(
        &self,
        request: Request<proto::PluginInfoRequest>,
    ) -> RpcResult<Response<proto::PluginInfoResponse>> {
        let request = request.into_inner();
        self.validate_identity(request.identity.as_ref())?;

        Err(Status::new(Code::Unimplemented, "todo"))
    }
}
