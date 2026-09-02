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

/// A directional focus request in pane geometry. This is deliberately kept in
/// the pane module so topology does not depend on the command layer.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PaneDirection {
    Left,
    Right,
    Up,
    Down,
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

    /// Finds the nearest leaf in the requested visual direction. The tree is
    /// laid out into normalized rectangles first, so focus remains spatially
    /// correct for nested and uneven splits instead of relying on traversal
    /// order.
    pub fn directional_neighbor(&self, target: PaneId, direction: PaneDirection) -> Option<PaneId> {
        let mut leaves = Vec::new();
        collect_leaf_bounds(self, PaneBounds::full(), &mut leaves);
        let current = leaves.iter().find(|leaf| leaf.id == target)?.bounds;
        leaves
            .into_iter()
            .filter(|leaf| leaf.id != target && is_in_direction(leaf.bounds, current, direction))
            .min_by(|left, right| {
                candidate_score(left.bounds, current, direction)
                    .partial_cmp(&candidate_score(right.bounds, current, direction))
                    .unwrap_or(std::cmp::Ordering::Equal)
            })
            .map(|leaf| leaf.id)
    }
}

#[derive(Debug, Clone, Copy)]
struct PaneBounds {
    left: f32,
    top: f32,
    right: f32,
    bottom: f32,
}

impl PaneBounds {
    const fn full() -> Self {
        Self {
            left: 0.0,
            top: 0.0,
            right: 1.0,
            bottom: 1.0,
        }
    }

    fn center_x(self) -> f32 {
        (self.left + self.right) / 2.0
    }

    fn center_y(self) -> f32 {
        (self.top + self.bottom) / 2.0
    }
}

#[derive(Debug, Clone, Copy)]
struct LeafBounds {
    id: PaneId,
    bounds: PaneBounds,
}

fn collect_leaf_bounds(node: &PaneNode, bounds: PaneBounds, output: &mut Vec<LeafBounds>) {
    match node {
        PaneNode::Leaf(id) => output.push(LeafBounds { id: *id, bounds }),
        PaneNode::Split {
            axis,
            ratio,
            first,
            second,
        } => {
            let ratio = ratio.clamp(0.05, 0.95);
            match axis {
                SplitAxis::Horizontal => {
                    let split_x = bounds.left + (bounds.right - bounds.left) * ratio;
                    collect_leaf_bounds(
                        first,
                        PaneBounds {
                            right: split_x,
                            ..bounds
                        },
                        output,
                    );
                    collect_leaf_bounds(
                        second,
                        PaneBounds {
                            left: split_x,
                            ..bounds
                        },
                        output,
                    );
                }
                SplitAxis::Vertical => {
                    let split_y = bounds.top + (bounds.bottom - bounds.top) * ratio;
                    collect_leaf_bounds(
                        first,
                        PaneBounds {
                            bottom: split_y,
                            ..bounds
                        },
                        output,
                    );
                    collect_leaf_bounds(
                        second,
                        PaneBounds {
                            top: split_y,
                            ..bounds
                        },
                        output,
                    );
                }
            }
        }
    }
}

const GEOMETRY_EPSILON: f32 = 0.0001;

fn is_in_direction(candidate: PaneBounds, current: PaneBounds, direction: PaneDirection) -> bool {
    match direction {
        PaneDirection::Left => candidate.right <= current.left + GEOMETRY_EPSILON,
        PaneDirection::Right => candidate.left >= current.right - GEOMETRY_EPSILON,
        PaneDirection::Up => candidate.bottom <= current.top + GEOMETRY_EPSILON,
        PaneDirection::Down => candidate.top >= current.bottom - GEOMETRY_EPSILON,
    }
}

fn candidate_score(
    candidate: PaneBounds,
    current: PaneBounds,
    direction: PaneDirection,
) -> (f32, f32, f32) {
    let (primary_gap, cross_gap, center_distance) = match direction {
        PaneDirection::Left => (
            (current.left - candidate.right).max(0.0),
            interval_gap(candidate.top, candidate.bottom, current.top, current.bottom),
            (current.center_x() - candidate.center_x()).abs()
                + (current.center_y() - candidate.center_y()).abs(),
        ),
        PaneDirection::Right => (
            (candidate.left - current.right).max(0.0),
            interval_gap(candidate.top, candidate.bottom, current.top, current.bottom),
            (current.center_x() - candidate.center_x()).abs()
                + (current.center_y() - candidate.center_y()).abs(),
        ),
        PaneDirection::Up => (
            (current.top - candidate.bottom).max(0.0),
            interval_gap(candidate.left, candidate.right, current.left, current.right),
            (current.center_x() - candidate.center_x()).abs()
                + (current.center_y() - candidate.center_y()).abs(),
        ),
        PaneDirection::Down => (
            (candidate.top - current.bottom).max(0.0),
            interval_gap(candidate.left, candidate.right, current.left, current.right),
            (current.center_x() - candidate.center_x()).abs()
                + (current.center_y() - candidate.center_y()).abs(),
        ),
    };
    (primary_gap, cross_gap, center_distance)
}

fn interval_gap(first_start: f32, first_end: f32, second_start: f32, second_end: f32) -> f32 {
    if first_end < second_start {
        second_start - first_end
    } else if second_end < first_start {
        first_start - second_end
    } else {
        0.0
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
