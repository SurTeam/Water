pub mod application;
pub mod control;
pub mod workspace;

pub use application::WaterApplication;
pub use control::{
    UiControlClient, UiControlReceiver, UiKeystrokeResult, UiSnapshot, ui_control_channel,
};
pub use workspace::WorkspaceView;
