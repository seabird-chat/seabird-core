use std::str::FromStr;

use sqlx::{sqlite::SqlitePoolOptions, SqlitePool};

use crate::prelude::*;

#[derive(Debug)]
pub struct AuthToken {
    pub id: i64,
    pub name: String,
    pub key: String,
    pub status: String,
}

#[derive(Debug)]
pub enum AuthTokenStatus {
    Connected,
    Disconnected,
    Unknown,
}

impl From<String> for ChannelLinkStatus {
    fn from(value: String) -> Self {
        match value.as_str() {
            "connected" => ChannelLinkStatus::Connected,
            "disconnected" => ChannelLinkStatus::Disconnected,
            _ => ChannelLinkStatus::Unknown,
        }
    }
}

#[derive(Debug)]
pub struct Channel {
    pub uuid: String,
    pub name: String,
}

#[derive(Debug)]
pub enum ChannelLinkStatus {
    Connected,
    Disconnected,
    Unknown,
}

impl From<String> for ChannelLinkStatus {
    fn from(value: String) -> Self {
        match value.as_str() {
            "connected" => ChannelLinkStatus::Connected,
            "disconnected" => ChannelLinkStatus::Disconnected,
            _ => ChannelLinkStatus::Unknown,
        }
    }
}

#[derive(Debug)]
pub struct ChannelLink {
    pub id: i64,
    pub auth_token_id: i64,
    pub channel_id: String,
    pub name: String,
    pub status: ChannelLinkStatus,
}

#[derive(Debug)]
pub struct DB {
    inner: SqlitePool,
}

impl DB {
    pub async fn new(database_url: &str) -> Result<DB> {
        Ok(DB {
            inner: SqlitePoolOptions::new().connect(database_url).await?,
        })
    }

    pub async fn get_auth_token(&self, key: &str) -> Result<Option<AuthToken>> {
        Ok(sqlx::query_as!(
            crate::db::AuthToken,
            "SELECT * FROM seabird_auth_tokens WHERE key = ? LIMIT 1",
            key,
        )
        .fetch_optional(&self.inner)
        .await?)
    }

    pub async fn get_channel(&self, key: &str) -> Result<Option<Channel>> {
        Ok(sqlx::query_as!(
            crate::db::Channel,
            "SELECT * FROM seabird_channels WHERE uuid = ? LIMIT 1",
            key,
        )
        .fetch_optional(&self.inner)
        .await?)
    }

    pub async fn get_channel_links(
        &self,
        auth_token_id: i64,
        channel_id: i64,
    ) -> Result<Vec<ChannelLink>> {
        Ok(sqlx::query_as!(
            crate::db::ChannelLink,
            "SELECT * FROM seabird_channel_links WHERE auth_token_id = ? AND channel_id = ?",
            auth_token_id,
            channel_id,
        )
        .fetch_all(&self.inner)
        .await?)
    }
}
