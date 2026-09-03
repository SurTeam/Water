use std::path::PathBuf;
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::{Deserialize, Serialize};

const UI_CONTROL_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Debug)]
pub(crate) enum UiControlRequest {
    Keystroke {
        keystroke: String,
        reply: Sender<Result<UiKeystrokeResult, String>>,
    },
    Snapshot {
        reply: Sender<Result<UiSnapshot, String>>,
    },
    Screenshot {
        path: PathBuf,
        reply: Sender<Result<UiScreenshot, String>>,
    },
    Wheel {
        position: (f32, f32),
        delta: (f32, f32),
        reply: Sender<Result<UiWheelResult, String>>,
    },
}

#[derive(Clone, Debug)]
pub struct UiControlClient {
    sender: Sender<UiControlRequest>,
}

#[derive(Debug)]
pub struct UiControlReceiver {
    receiver: Arc<Mutex<Receiver<UiControlRequest>>>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct UiKeystrokeResult {
    pub keystroke: String,
    pub handled: bool,
    pub window_count: usize,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct UiSnapshot {
    pub window_count: usize,
    pub has_active_window: bool,
}

/// Metadata returned after the current Water window has been rendered to an
/// image at the requested path.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct UiScreenshot {
    pub path: String,
    pub width: u32,
    pub height: u32,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct UiWheelResult {
    pub position: (f32, f32),
    pub delta: (f32, f32),
    pub propagate: bool,
    pub default_prevented: bool,
}

pub fn ui_control_channel() -> (UiControlClient, UiControlReceiver) {
    let (sender, receiver) = mpsc::channel();
    (
        UiControlClient { sender },
        UiControlReceiver {
            receiver: Arc::new(Mutex::new(receiver)),
        },
    )
}

impl UiControlClient {
    pub fn dispatch_keystroke(
        &self,
        keystroke: impl Into<String>,
    ) -> Result<UiKeystrokeResult, String> {
        let (reply, response) = mpsc::channel();
        self.sender
            .send(UiControlRequest::Keystroke {
                keystroke: keystroke.into(),
                reply,
            })
            .map_err(|_| "UI control channel is unavailable".to_owned())?;
        response
            .recv_timeout(UI_CONTROL_TIMEOUT)
            .map_err(|_| "timed out waiting for the GPUI thread".to_owned())?
    }

    pub fn wheel(&self, position: (f32, f32), delta: (f32, f32)) -> Result<UiWheelResult, String> {
        let (reply, response) = mpsc::channel();
        self.sender
            .send(UiControlRequest::Wheel {
                position,
                delta,
                reply,
            })
            .map_err(|_| "UI control channel is unavailable".to_owned())?;
        response
            .recv_timeout(UI_CONTROL_TIMEOUT)
            .map_err(|_| "timed out waiting for the GPUI thread".to_owned())?
    }

    pub fn snapshot(&self) -> Result<UiSnapshot, String> {
        let (reply, response) = mpsc::channel();
        self.sender
            .send(UiControlRequest::Snapshot { reply })
            .map_err(|_| "UI control channel is unavailable".to_owned())?;
        response
            .recv_timeout(UI_CONTROL_TIMEOUT)
            .map_err(|_| "timed out waiting for the GPUI thread".to_owned())?
    }

    pub fn screenshot(&self, path: impl Into<PathBuf>) -> Result<UiScreenshot, String> {
        let (reply, response) = mpsc::channel();
        self.sender
            .send(UiControlRequest::Screenshot {
                path: path.into(),
                reply,
            })
            .map_err(|_| "UI control channel is unavailable".to_owned())?;
        response
            .recv_timeout(UI_CONTROL_TIMEOUT)
            .map_err(|_| "timed out waiting for the GPUI thread".to_owned())?
    }
}

impl UiControlReceiver {
    pub(crate) fn recv(&self) -> Option<UiControlRequest> {
        self.receiver
            .lock()
            .expect("UI control receiver poisoned")
            .recv()
            .ok()
    }
}
