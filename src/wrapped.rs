use tokio_stream::Stream;
use tokio::sync::{mpsc, oneshot};

use crate::prelude::*;

// channel returns a writer, reader, and a notification channel. Send/receive
// mechanics are the same as an mpsc::channel, but there is an additional
// oneshot notification channel which will fire when the receiver is dropped.
// This is a workaround which lets us detect when a client has cleanly
// disconnected with tonic gRPC streams.
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

// WrappedChannelSender is the Send side of a wrapped channel.
#[derive(Debug, Clone)]
pub struct WrappedChannelSender<T> {
    inner: mpsc::Sender<T>,
}

impl<T> WrappedChannelSender<T> {
    pub async fn send(&mut self, value: T) -> Result<(), mpsc::error::SendError<T>> {
        self.inner.send(value).await
    }
}

// WrappedChannelSender is the Stream side of a wrapped channel.
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
                error!("failed to notify");
            }
        } else {
            error!("oneshot already triggered");
        }
    }
}
