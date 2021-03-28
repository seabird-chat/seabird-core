use crate::prelude::*;

use hyper::{Body, Request as HyperRequest, Response as HyperResponse};
use tonic::{body::BoxBody, transport::NamedService, Request};
use tower::Service;

const X_AUTH_USERNAME: &str = "x-auth-username";

pub fn extract_auth_username<T>(req: &Request<T>) -> RpcResult<String> {
    Ok(req
        .metadata()
        .get(X_AUTH_USERNAME)
        .ok_or_else(|| Status::internal("missing auth username"))?
        .to_str()
        .map_err(|_| Status::internal("failed to decode auth username"))?
        .to_string())
}

// AuthedService is a frustratingly necessary evil because there is no async
// tonic::Interceptor. This means we can't .await on the RwLock protecting
// server.tokens. We get around this by implementing a middleware which pulls
// the request header out (since the http2 headers map to gRPC request metadata
// directly) to check it before we even get into the gRPC/Tonic code.
#[derive(Debug, Clone)]
pub struct AuthedService<S> {
    server: Arc<super::Server>,
    inner: S,
}

impl<S> AuthedService<S> {
    pub fn new(server: Arc<super::Server>, inner: S) -> Self {
        AuthedService { server, inner }
    }
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
            let (mut req, username): (HyperRequest<Body>, tonic::codegen::http::HeaderValue) =
                match req.headers().get("authorization") {
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
                                    Some(maybe_username) => match maybe_username.parse() {
                                        Ok(username) => (req, username),
                                        Err(_) => {
                                            return Ok(Status::internal(
                                                "invalid auth token username",
                                            )
                                            .to_http())
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
                    None => {
                        return Ok(Status::unauthenticated("missing authorization header").to_http())
                    }
                };

            let username_str = match username.to_str() {
                Ok(username) => username,
                Err(_) => return Ok(Status::internal("failed to parse tag").to_http()),
            };

            info!(
                "Authenticated request by user {}: {}",
                username_str,
                req.uri()
            );

            req.headers_mut().insert(X_AUTH_USERNAME, username);

            let resp = svc.call(req).await?;

            info!("Sending response: {:?}", resp.headers());

            Ok(resp)
        })
    }
}

impl<S: NamedService> NamedService for AuthedService<S> {
    const NAME: &'static str = S::NAME;
}
