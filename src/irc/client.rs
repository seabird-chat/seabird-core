use std::boxed::Box;
use std::pin::Pin;

use tokio::io::{AsyncBufRead, AsyncBufReadExt, AsyncWrite, AsyncWriteExt, BufReader};
use tokio::net::TcpStream;
use tokio::sync::{Mutex, RwLock};
use tokio_rustls::{
    rustls::{
        Certificate, ClientConfig, RootCertStore, ServerCertVerified, ServerCertVerifier, TLSError,
    },
    webpki::DNSNameRef,
    TlsConnector,
};
use url::Url;

use crate::prelude::*;

type BoxedReader = Pin<Box<dyn AsyncBufRead>>;
type BoxedWriter = Pin<Box<dyn AsyncWrite>>;

struct UnsafeVerifier {}

impl UnsafeVerifier {
    fn new() -> Arc<Self> {
        Arc::new(Self {})
    }
}

impl ServerCertVerifier for UnsafeVerifier {
    fn verify_server_cert(
        &self,
        _roots: &RootCertStore,
        _presented_certs: &[Certificate],
        _dns_name: DNSNameRef<'_>,
        _ocsp_response: &[u8],
    ) -> std::result::Result<ServerCertVerified, TLSError> {
        Ok(ServerCertVerified::assertion())
    }
}

pub struct Client {
    reader: Mutex<BoxedReader>,
    writer: Mutex<BoxedWriter>,
    metadata: RwLock<Arc<ClientMetadata>>,
}

pub struct ClientMetadata {
    pub connected: bool,
    pub current_nick: String,
}

impl ClientMetadata {
    fn new(connected: bool, current_nick: String) -> Arc<Self> {
        Arc::new(ClientMetadata {
            connected,
            current_nick,
        })
    }
}

impl Client {
    pub async fn new(
        host: String,
        nick: String,
        user: Option<String>,
        name: Option<String>,
        pass: Option<String>,
    ) -> Result<Self> {
        let user = user.unwrap_or_else(|| nick.clone());
        let name = name.unwrap_or_else(|| user.clone());

        let url = Url::parse(host.as_str())?;

        let (tls, unsafe_tls, default_port) = match url.scheme() {
            "ircs" => (true, false, 6697),
            "ircs+unsafe" => (true, true, 6697),
            "irc" => (false, false, 6667),
            _ => anyhow::bail!("unknown irc host scheme"),
        };

        // NOTE: this isn't async, but dns lookup should be fast, this happens
        // only on startup, and we get this helpful "default port goodness".
        let addr = url
            .socket_addrs(|| Some(default_port))
            .context("failed to lookup IRC host")?
            .into_iter()
            .next()
            .context("failed to lookup IRC host")?;

        let host_name = url
            .domain()
            .ok_or_else(|| anyhow!("irc host missing domain"))?;

        let socket = TcpStream::connect(&addr).await?;

        let (reader, writer): (BoxedReader, BoxedWriter) = if tls {
            let dns = DNSNameRef::try_from_ascii_str(host_name)?;

            let mut client_config = ClientConfig::new();

            if unsafe_tls {
                client_config
                    .dangerous()
                    .set_certificate_verifier(UnsafeVerifier::new());
            } else {
                client_config.root_store =
                    rustls_native_certs::load_native_certs().map_err(|e| e.1)?;
            }

            let connector = TlsConnector::from(Arc::new(client_config));
            let (reader, writer) = tokio::io::split(connector.connect(dns, socket).await?);

            (Box::pin(BufReader::new(reader)), Box::pin(writer))
        } else {
            let (reader, writer) = tokio::io::split(socket);

            (Box::pin(BufReader::new(reader)), Box::pin(writer))
        };

        let client = Client {
            reader: Mutex::new(reader),
            writer: Mutex::new(writer),
            metadata: RwLock::new(ClientMetadata::new(false, nick.clone())),
        };

        // Now that we have a connection, handle sending the handshake messages

        if let Some(pass) = pass {
            client.write(format!("PASS :{}", pass)).await?;
        }

        client.write(format!("NICK :{}", nick)).await?;
        client
            .write(format!("USER {} 0.0.0.0 0.0.0.0 :{}", user, name))
            .await?;

        Ok(client)
    }

    pub async fn write(&self, msg: String) -> Result<()> {
        trace!("--> {}", msg);

        let mut guard = self.writer.lock().await;

        guard.write_all(msg.as_bytes()).await?;
        guard.write_all(b"\r\n").await?;

        Ok(())
    }

    pub async fn write_message(&self, msg: &irc::Message) -> Result<()> {
        self.write(msg.to_string()).await
    }

    pub async fn recv(&self) -> Result<irc::Message> {
        let mut buf = String::new();
        self.reader.lock().await.read_line(&mut buf).await?;

        let msg: irc::Message = buf.parse()?;

        trace!("<-- {}", msg);

        match msg.command.as_str() {
            // From rfc2812 section 5.1 (Command responses)
            //
            // 001    RPL_WELCOME
            //        "Welcome to the Internet Relay Network
            //        <nick>!<user>@<host>"
            "001" if !msg.params.is_empty() => {
                let mut meta = self.metadata.write().await;

                // Mark the connection as connected and copy what the server
                // told us our name is.
                *meta = ClientMetadata::new(true, msg.params[0].clone());
            }

            // From rfc2812 section 5.2 (Error Replies)
            //
            // 433    ERR_NICKNAMEINUSE
            //        "<nick> :Nickname is already in use"
            //
            // 437    ERR_UNAVAILRESOURCE
            //        "<nick/channel> :Nick/channel is temporarily unavailable"
            "433" | "437" => {
                let mut meta = self.metadata.write().await;
                // We only want to update the nick if we're not already
                // connected.
                if !meta.connected {
                    *meta = ClientMetadata::new(false, format!("{}_", meta.current_nick));
                    self.write(format!("NICK :{}", meta.current_nick)).await?;
                }
            }

            "NICK" => {
                let mut meta = self.metadata.write().await;
                if let Some(prefix) = msg.prefix.as_ref() {
                    if prefix.nick == meta.current_nick {
                        *meta = ClientMetadata::new(meta.connected, msg.params[0].clone());
                    }
                }
            }

            "PING" => {
                self.write_message(&irc::Message::new(
                    "PONG".to_string(),
                    msg.params.iter().map(String::from).collect(),
                ))
                .await?
            }
            _ => {}
        }

        Ok(msg)
    }
}
