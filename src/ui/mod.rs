#[cfg(feature = "gui")]
pub mod application;
#[cfg_attr(not(feature = "gui"), allow(dead_code))]
pub mod control;
#[cfg(feature = "gui")]
pub mod settings;
#[cfg(feature = "gui")]
pub mod workspace;

#[cfg(feature = "gui")]
pub use application::WaterApplication;
pub use control::{
    UiControlClient, UiControlReceiver, UiKeystrokeResult, UiScreenshot, UiSnapshot, UiWheelResult,
    ui_control_channel,
};
#[cfg(feature = "gui")]
pub use settings::SettingsView;
#[cfg(feature = "gui")]
pub use workspace::WorkspaceView;
