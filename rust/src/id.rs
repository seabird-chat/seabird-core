use std::str::FromStr;

use percent_encoding::{percent_decode, percent_encode, NON_ALPHANUMERIC};

#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FullId {
    pub backend: BackendId,
    pub path: String,
}

impl FullId {
    pub fn new(backend: BackendId, path: String) -> Self {
        FullId { backend, path }
    }

    pub fn into_inner(self) -> (BackendId, String) {
        return (self.backend, self.path);
    }
}

#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct BackendId {
    pub scheme: String,
    pub id: String,
}

impl BackendId {
    pub fn new(scheme: String, id: String) -> Self {
        BackendId { scheme, id }
    }

    pub fn relative(&self, path: String) -> FullId {
        FullId::new(self.clone(), path)
    }
}

impl std::fmt::Display for FullId {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "{}/{}",
            self.backend,
            percent_encode(self.path.as_ref(), NON_ALPHANUMERIC)
        )
    }
}

impl std::fmt::Display for BackendId {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "{}://{}",
            percent_encode(self.scheme.as_ref(), NON_ALPHANUMERIC),
            percent_encode(self.id.as_ref(), NON_ALPHANUMERIC)
        )
    }
}

impl FromStr for BackendId {
    type Err = crate::error::Error;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        let mut split = s.splitn(2, "://");

        match (split.next(), split.next()) {
            (Some(scheme), Some(id)) => Ok(BackendId::new(
                percent_decode(scheme.as_ref()).decode_utf8()?.into_owned(),
                percent_decode(id.as_ref()).decode_utf8()?.into_owned(),
            )),
            (Some(_), None) => anyhow::bail!("missing id part"),
            (None, _) => anyhow::bail!("invalid id"),
        }
    }
}

impl FromStr for FullId {
    type Err = crate::error::Error;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        let mut split = s.splitn(2, "://");

        match (split.next(), split.next()) {
            (Some(scheme), Some(id)) => {
                let mut split = id.splitn(2, "/");

                match (split.next(), split.next()) {
                    (Some(id), Some(rel)) => Ok(FullId::new(
                        BackendId::new(
                            percent_decode(scheme.as_ref()).decode_utf8()?.into_owned(),
                            percent_decode(id.as_ref()).decode_utf8()?.into_owned(),
                        ),
                        percent_decode(rel.as_ref()).decode_utf8()?.into_owned(),
                    )),
                    (Some(_), None) => anyhow::bail!("missing path part"),
                    (None, _) => anyhow::bail!("missing id part"),
                }
            }
            (Some(_), None) => anyhow::bail!("missing id part"),
            (None, _) => anyhow::bail!("invalid id"),
        }
    }
}
