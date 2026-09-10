use std::{fmt, str::FromStr};

use serde::{Deserialize, Deserializer, Serialize};
use uuid::Uuid;

macro_rules! define_id {
    ($name:ident) => {
        #[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize)]
        #[serde(transparent)]
        pub struct $name(Uuid);

        impl $name {
            /// Deterministic IDs for fixtures and reserved local identities.
            pub const fn new(value: u64) -> Self {
                Self(Uuid::from_u128(value as u128))
            }

            pub const fn from_u128(value: u128) -> Self {
                Self(Uuid::from_u128(value))
            }

            pub const fn get(self) -> u128 {
                self.0.as_u128()
            }
        }

        impl From<Uuid> for $name {
            fn from(value: Uuid) -> Self {
                Self(value)
            }
        }

        impl From<u64> for $name {
            fn from(value: u64) -> Self {
                Self::new(value)
            }
        }

        impl FromStr for $name {
            type Err = uuid::Error;
            fn from_str(value: &str) -> Result<Self, Self::Err> {
                Uuid::parse_str(value).map(Self)
            }
        }

        impl<'de> Deserialize<'de> for $name {
            fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
                // Numeric IDs remain readable for existing scenario fixtures.
                // New wire output always uses lossless UUID strings.
                #[derive(Deserialize)]
                #[serde(untagged)]
                enum Input {
                    Uuid(Uuid),
                    Legacy(u64),
                }
                Ok(match Input::deserialize(deserializer)? {
                    Input::Uuid(value) => Self(value),
                    Input::Legacy(value) => Self::new(value),
                })
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

/// Allocates full UUIDv4 values without truncation.
#[derive(Debug, Default, Clone)]
pub struct IdAllocator;

impl IdAllocator {
    pub fn new() -> Self {
        Self
    }

    pub fn alloc<T: From<Uuid>>(&mut self) -> T {
        T::from(Uuid::new_v4())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn full_uuid_round_trips_through_json_and_cli() {
        let raw = Uuid::parse_str("fedcba98-7654-4321-8abc-def012345678").unwrap();
        let id = TerminalId::from(raw);
        assert_eq!(id.get(), raw.as_u128());
        let json = serde_json::to_string(&id).unwrap();
        assert_eq!(json, format!("\"{raw}\""));
        assert_eq!(serde_json::from_str::<TerminalId>(&json).unwrap(), id);
        assert_eq!(id.to_string().parse::<TerminalId>().unwrap(), id);
        assert_eq!(
            serde_json::from_str::<TerminalId>("9").unwrap(),
            TerminalId::new(9)
        );
    }

    #[test]
    fn allocator_preserves_uuid_version_and_variant() {
        let id: PaneId = IdAllocator::new().alloc();
        let uuid = Uuid::from_u128(id.get());
        assert_eq!(uuid.get_version_num(), 4);
        assert_eq!(uuid.get_variant(), uuid::Variant::RFC4122);
        assert_ne!(id.get() >> 64, 0);
    }
}
