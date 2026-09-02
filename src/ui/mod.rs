pub mod application;
pub mod control;
pub mod settings;
pub mod workspace;

pub use application::WaterApplication;
pub use control::{
    UiControlClient, UiControlReceiver, UiKeystrokeResult, UiScreenshot, UiSnapshot,
    ui_control_channel,
};
pub use settings::SettingsView;
pub use workspace::WorkspaceView;
