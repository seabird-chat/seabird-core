use std::str::FromStr;

use sqlx::{
    SqlitePool,
    sqlite::{SqliteConnectOptions, SqlitePoolOptions},
};

use crate::prelude::*;

pub struct AuthToken {
    pub id: i64,
    pub name: String,
    pub key: String,
}

#[derive(Debug)]
pub struct DB {
    inner: SqlitePool,
}

impl DB {
    pub async fn new(database_url: &str) -> Result<DB> {
        let opts = SqliteConnectOptions::from_str(database_url)?.create_if_missing(true);
        let inner = SqlitePoolOptions::new().connect_with(opts).await?;

        sqlx::migrate!().run(&inner).await?;

        Ok(DB { inner })
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
}
