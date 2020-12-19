use async_trait::async_trait;
use tokio::stream::Stream;
use tokio::sync::{mpsc, oneshot};

use crate::prelude::*;

pub fn channel<T>(
    buffer_size: usize,
) -> (
    WrappedChannelSender<T>,
    WrappedChannelReceiver<T>,
    oneshot::Receiver<()>,
) {
    let (sender, receiver) = mpsc::channel(buffer_size);
    let (oneshot_sender, oneshot_receiver) = oneshot::channel();

    (
        WrappedChannelSender { inner: sender },
        WrappedChannelReceiver {
            inner: receiver,
            oneshot: Some(oneshot_sender),
        },
        oneshot_receiver,
    )
}

#[derive(Debug, Clone)]
pub struct WrappedChannelSender<T> {
    inner: mpsc::Sender<T>,
}

impl<T> WrappedChannelSender<T> {
    pub async fn send(&mut self, value: T) -> Result<(), mpsc::error::SendError<T>> {
        self.inner.send(value).await
    }
}

#[derive(Debug)]
pub struct WrappedChannelReceiver<T> {
    inner: mpsc::Receiver<T>,
    oneshot: Option<oneshot::Sender<()>>,
}

impl<T> Stream for WrappedChannelReceiver<T> {
    type Item = T;

    fn poll_next(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Option<Self::Item>> {
        self.inner.poll_recv(cx)
    }
}

impl<T> Drop for WrappedChannelReceiver<T> {
    fn drop(&mut self) {
        if let Some(oneshot) = self.oneshot.take() {
            if let Err(_err) = oneshot.send(()) {
                warn!("failed to notify");
            }
        } else {
            warn!("oneshot already triggered");
        }
    }
}

#[async_trait]
pub trait GenericSender<T> {
    async fn send_value(&mut self, value: T) -> Result<(), mpsc::error::SendError<T>>;
}

#[async_trait]
impl<T> GenericSender<T> for mpsc::Sender<T> where
T: Send{
    async fn send_value(&mut self, value: T) -> Result<(), mpsc::error::SendError<T>> {
        self.send(value).await
    }
}

#[async_trait]
impl<T> GenericSender<T> for WrappedChannelSender<T>
where
    T: Send,
{
    async fn send_value(&mut self, value: T) -> Result<(), mpsc::error::SendError<T>> {
        self.send(value).await
    }
}
