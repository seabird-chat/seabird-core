pub mod common {
    tonic::include_proto!("common");

    impl User {
        pub fn into_relative(self, backend_id: &crate::BackendId) -> Self {
            User {
                id: backend_id.relative(self.id).to_string(),
                display_name: self.display_name,
            }
        }
    }

    impl ChannelSource {
        pub fn into_relative(self, backend_id: &crate::BackendId) -> Self {
            ChannelSource {
                channel_id: backend_id.relative(self.channel_id).to_string(),
                user: self.user.map(|user| user.into_relative(backend_id)),
            }
        }
    }
}

pub mod seabird {
    tonic::include_proto!("seabird");
}

// Re-export all the types in each module for convenience, as well as adding a
// few aliases for some deeply nested types.

pub use self::common::*;
pub use self::seabird::*;

pub use self::chat_event::Inner as ChatEventInner;
pub use self::chat_request::Inner as ChatRequestInner;
pub use self::event::Inner as EventInner;
