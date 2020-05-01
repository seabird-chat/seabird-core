pub type Error = anyhow::Error;
pub type Result<T> = std::result::Result<T, Error>;
pub type RpcResult<T> = std::result::Result<T, tonic::Status>;
