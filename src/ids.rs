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
            fn from(value: $name) -> u64 {
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

/// Allocates globally unique random values (UUIDv4, truncated to 64 bits)
/// and converts them into a typed ID.
///
/// Random allocation (instead of a per-process counter) makes IDs unique
/// across independent servers: a local GUI process and each remote water
/// server draw from the same 122-bit v4 space, so IDs from different
/// connections never collide. The allocator is still owned by the
/// model/dispatcher so IDs remain stable for the lifetime of the
/// application without making a vector position part of identity.
#[derive(Debug, Default, Clone)]
pub struct IdAllocator;

impl IdAllocator {
    pub fn new() -> Self {
        Self
    }

    pub fn alloc<T>(&mut self) -> T
    where
        T: From<u64>,
    {
        // The upper 64 bits of a v4 UUID carry the 48-bit timestamp plus the
        // version/variant-random content; using them instead of either half
        // keeps the draw as random as possible.
        T::from((uuid::Uuid::new_v4().as_u128() >> 64) as u64)
    }
}
