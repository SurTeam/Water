use std::fmt;

use serde::{Deserialize, Serialize};

macro_rules! define_id {
    ($name:ident) => {
        #[derive(
            Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize,
        )]
        #[serde(transparent)]
        pub struct $name(u64);

        impl $name {
            pub const fn new(value: u64) -> Self {
                Self(value)
            }

            pub const fn get(self) -> u64 {
                self.0
            }
        }

        impl From<u64> for $name {
            fn from(value: u64) -> Self {
                Self::new(value)
            }
        }

        impl From<$name> for u64 {
            fn from(value: $name) -> Self {
                value.get()
            }
        }

        impl fmt::Display for $name {
            fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                self.0.fmt(formatter)
            }
        }
    };
}

define_id!(WorkspaceId);
define_id!(TabId);
define_id!(PaneId);
define_id!(SurfaceId);
define_id!(TerminalId);
define_id!(SessionId);
define_id!(ConnectionId);
define_id!(OperationId);

/// Allocates monotonically increasing values and converts them into a typed ID.
///
/// The allocator is owned by the model/dispatcher, so IDs remain stable for the
/// lifetime of an application without making a vector position part of identity.
#[derive(Debug, Default, Clone)]
pub struct IdAllocator {
    next: u64,
}

impl IdAllocator {
    pub fn new() -> Self {
        Self { next: 1 }
    }

    pub fn alloc<T>(&mut self) -> T
    where
        T: From<u64>,
    {
        let value = self.next;
        self.next = self
            .next
            .checked_add(1)
            .expect("water ID allocator exhausted");
        T::from(value)
    }
}
