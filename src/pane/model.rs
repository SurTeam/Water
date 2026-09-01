use serde::{Deserialize, Serialize};

use crate::ids::{PaneId, SurfaceId};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SplitAxis {
    /// Children are laid out from left to right.
    Horizontal,
    /// Children are laid out from top to bottom.
    Vertical,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum PaneNode {
    Leaf(PaneId),
    Split {
        axis: SplitAxis,
        ratio: f32,
        first: Box<PaneNode>,
        second: Box<PaneNode>,
    },
}

impl PaneNode {
    pub fn leaf(pane_id: PaneId) -> Self {
        Self::Leaf(pane_id)
    }

    pub fn contains(&self, pane_id: PaneId) -> bool {
        match self {
            Self::Leaf(id) => *id == pane_id,
            Self::Split { first, second, .. } => {
                first.contains(pane_id) || second.contains(pane_id)
            }
        }
    }

    pub fn pane_count(&self) -> usize {
        match self {
            Self::Leaf(_) => 1,
            Self::Split { first, second, .. } => first.pane_count() + second.pane_count(),
        }
    }

    pub fn leaf_ids(&self, output: &mut Vec<PaneId>) {
        match self {
            Self::Leaf(id) => output.push(*id),
            Self::Split { first, second, .. } => {
                first.leaf_ids(output);
                second.leaf_ids(output);
            }
        }
    }

    pub fn split_leaf(
        &mut self,
        target: PaneId,
        axis: SplitAxis,
        ratio: f32,
        new_pane: PaneId,
        new_first: bool,
    ) -> bool {
        match self {
            Self::Leaf(id) if *id == target => {
                let existing = Self::Leaf(*id);
                let created = Self::Leaf(new_pane);
                let (first, second) = if new_first {
                    (created, existing)
                } else {
                    (existing, created)
                };
                *self = Self::Split {
                    axis,
                    ratio,
                    first: Box::new(first),
                    second: Box::new(second),
                };
                true
            }
            Self::Leaf(_) => false,
            Self::Split { first, second, .. } => {
                first.split_leaf(target, axis, ratio, new_pane, new_first)
                    || second.split_leaf(target, axis, ratio, new_pane, new_first)
            }
        }
    }

    /// Removes a leaf and collapses its parent split. The root is guaranteed to
    /// remain a leaf by the caller, which rejects closing the final pane.
    pub fn close_leaf(&mut self, target: PaneId) -> bool {
        let original = std::mem::replace(self, Self::Leaf(target));
        let (replacement, removed) = remove_leaf(original, target);
        if let Some(replacement) = replacement {
            *self = replacement;
        }
        removed
    }

    /// Resizes the nearest split containing `target`. A split has no separate
    /// identity in Phase 1, so a leaf is the stable command target.
    pub fn resize_nearest(&mut self, target: PaneId, ratio: f32) -> bool {
        match self {
            Self::Leaf(_) => false,
            Self::Split {
                first,
                second,
                ratio: current_ratio,
                ..
            } => {
                let in_first = first.contains(target);
                let in_second = second.contains(target);
                if !in_first && !in_second {
                    return false;
                }

                if (in_first && first.resize_nearest(target, ratio))
                    || (in_second && second.resize_nearest(target, ratio))
                {
                    true
                } else {
                    *current_ratio = ratio;
                    true
                }
            }
        }
    }

    pub fn ratios(&self, output: &mut Vec<f32>) {
        match self {
            Self::Leaf(_) => {}
            Self::Split {
                ratio,
                first,
                second,
                ..
            } => {
                output.push(*ratio);
                first.ratios(output);
                second.ratios(output);
            }
        }
    }
}

fn remove_leaf(node: PaneNode, target: PaneId) -> (Option<PaneNode>, bool) {
    match node {
        PaneNode::Leaf(id) if id == target => (None, true),
        PaneNode::Leaf(id) => (Some(PaneNode::Leaf(id)), false),
        PaneNode::Split {
            axis,
            ratio,
            mut first,
            mut second,
        } => {
            if first.contains(target) {
                let first_node = std::mem::replace(&mut first, Box::new(PaneNode::Leaf(target)));
                let (new_first, first_removed) = remove_leaf(*first_node, target);
                debug_assert!(first_removed);
                let replacement = match new_first {
                    Some(new_first) => PaneNode::Split {
                        axis,
                        ratio,
                        first: Box::new(new_first),
                        second,
                    },
                    None => *second,
                };
                return (Some(replacement), true);
            }

            if second.contains(target) {
                let second_node = std::mem::replace(&mut second, Box::new(PaneNode::Leaf(target)));
                let (new_second, second_removed) = remove_leaf(*second_node, target);
                debug_assert!(second_removed);
                let replacement = match new_second {
                    Some(new_second) => PaneNode::Split {
                        axis,
                        ratio,
                        first,
                        second: Box::new(new_second),
                    },
                    None => *first,
                };
                return (Some(replacement), true);
            }

            (
                Some(PaneNode::Split {
                    axis,
                    ratio,
                    first,
                    second,
                }),
                false,
            )
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Pane {
    pub id: PaneId,
    pub surface: SurfaceId,
}
