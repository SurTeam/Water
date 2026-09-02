use std::cell::RefCell;
use std::rc::Rc;
use std::sync::Arc;

use gpui::{
    App, AppContext, Bounds, Focusable, KeyBinding, Keystroke, Menu, MenuItem, QuitMode,
    SystemMenuType, Task, TitlebarOptions, WeakEntity, WindowBounds, WindowDecorations,
    WindowOptions, actions, point, px, size,
};

use crate::app::{CommandClient, ModelSnapshot, ModelSnapshotReceiver};
use crate::config::AppConfig;

use super::WorkspaceView;
use super::control::{UiControlReceiver, UiControlRequest, UiKeystrokeResult, UiSnapshot};

actions!(
    water,
    [
        NewWindow,
        HideWindow,
        MinimizeWindow,
        IgnoreQuit,
        NewTerminalTab,
        SplitRight,
        SplitDown
    ]
);

#[derive(Clone)]
pub struct WaterApplication {
    state: Rc<WaterApplicationState>,
}

struct WaterApplicationState {
    client: CommandClient,
    config: AppConfig,
    snapshot: RefCell<ModelSnapshot>,
    views: RefCell<Vec<WeakEntity<WorkspaceView>>>,
}

impl WaterApplication {
    pub fn new(client: CommandClient, snapshot: ModelSnapshot, config: AppConfig) -> Self {
        Self {
            state: Rc::new(WaterApplicationState {
                client,
                config,
                snapshot: RefCell::new(snapshot),
                views: RefCell::new(Vec::new()),
            }),
        }
    }

    pub fn install(
        &self,
        cx: &mut App,
        snapshot_receiver: ModelSnapshotReceiver,
        ui_control_receiver: UiControlReceiver,
    ) {
        cx.set_quit_mode(QuitMode::Explicit);
        cx.intercept_keystrokes(|event, _window, cx| {
            let keystroke = &event.keystroke;
            if keystroke.modifiers.platform
                && !keystroke.modifiers.control
                && !keystroke.modifiers.alt
                && !keystroke.modifiers.shift
                && keystroke.key == "q"
            {
                cx.stop_propagation();
            }
        })
        .detach();
        cx.bind_keys(window_key_bindings());

        let application = self.clone();
        cx.on_action(move |_: &NewWindow, cx| application.open_window(cx));
        cx.set_menus(application_menus());

        self.spawn_snapshot_listener(cx, snapshot_receiver).detach();
        self.spawn_ui_control_listener(cx, ui_control_receiver)
            .detach();
        self.open_window(cx);
        cx.activate(true);
    }

    pub fn reopen(&self, cx: &mut App) {
        if cx.windows().is_empty() {
            self.open_window(cx);
        }
        cx.activate(true);
    }

    pub fn open_window(&self, cx: &mut App) {
        let root = cx.new(|cx| {
            WorkspaceView::new_with_config(
                self.state.client.clone(),
                self.state.snapshot.borrow().clone(),
                cx.focus_handle(),
                self.state.config.clone(),
            )
        });
        let weak_root = root.downgrade();
        let focus_handle = root.read(cx).focus_handle(cx);
        let bounds = Bounds::centered(None, size(px(1100.), px(760.)), cx);
        match cx.open_window(water_window_options(bounds), move |window, cx| {
            window.activate_window();
            window.focus(&focus_handle, cx);
            root
        }) {
            Ok(_) => self.state.views.borrow_mut().push(weak_root),
            Err(error) => tracing::error!(
                target: "water::ui",
                ?error,
                "failed to open water window"
            ),
        }
    }

    fn spawn_snapshot_listener(&self, cx: &mut App, receiver: ModelSnapshotReceiver) -> Task<()> {
        let receiver = Arc::new(receiver);
        let state = self.state.clone();
        cx.spawn(async move |cx| {
            loop {
                let receiver_for_worker = receiver.clone();
                let snapshot = cx
                    .background_executor()
                    .spawn(async move { receiver_for_worker.recv().ok() })
                    .await;
                let Some(snapshot) = snapshot else {
                    break;
                };

                state.snapshot.replace(snapshot.clone());
                let views = state.views.borrow().clone();
                let mut live_views = Vec::with_capacity(views.len());
                for view in views {
                    if view
                        .update(cx, |workspace, cx| {
                            workspace.install_snapshot(snapshot.clone(), cx);
                        })
                        .is_ok()
                    {
                        live_views.push(view);
                    }
                }
                state.views.replace(live_views);
            }
        })
    }

    fn spawn_ui_control_listener(&self, cx: &mut App, receiver: UiControlReceiver) -> Task<()> {
        let receiver = Arc::new(receiver);
        cx.spawn(async move |cx| {
            loop {
                let receiver_for_worker = receiver.clone();
                let request = cx
                    .background_executor()
                    .spawn(async move { receiver_for_worker.recv() })
                    .await;
                let Some(request) = request else {
                    break;
                };

                match request {
                    UiControlRequest::Keystroke { keystroke, reply } => {
                        let result = cx.update(|cx| {
                            dispatch_controlled_keystroke(&keystroke, cx).map(|handled| {
                                UiKeystrokeResult {
                                    keystroke,
                                    handled,
                                    window_count: cx.windows().len(),
                                }
                            })
                        });
                        let _ = reply.send(result);
                    }
                    UiControlRequest::Snapshot { reply } => {
                        let result = cx.update(|cx| {
                            Ok(UiSnapshot {
                                window_count: cx.windows().len(),
                                has_active_window: cx.active_window().is_some(),
                            })
                        });
                        let _ = reply.send(result);
                    }
                }
            }
        })
    }
}

fn water_window_options(bounds: Bounds<gpui::Pixels>) -> WindowOptions {
    WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(bounds)),
        // Water renders the titlebar inside WorkspaceView. The transparent
        // titlebar keeps the platform traffic lights while hiding the native
        // title text/background, and app-owned dragging avoids AppKit's native
        // titlebar click delay.
        titlebar: Some(TitlebarOptions {
            title: None,
            appears_transparent: true,
            traffic_light_position: Some(point(px(9.), px(9.))),
        }),
        app_owns_titlebar_drag: true,
        window_decorations: Some(WindowDecorations::Client),
        ..Default::default()
    }
}

fn dispatch_controlled_keystroke(source: &str, cx: &mut App) -> Result<bool, String> {
    let keystroke = Keystroke::parse(source).map_err(|error| error.to_string())?;
    let window = cx
        .active_window()
        .or_else(|| {
            cx.window_stack()
                .and_then(|windows| windows.into_iter().next())
        })
        .or_else(|| cx.windows().into_iter().last())
        .ok_or_else(|| "Water has no open window".to_owned())?;
    window
        .update(cx, |_, window, cx| window.dispatch_keystroke(keystroke, cx))
        .map_err(|error| error.to_string())
}

fn window_key_bindings() -> Vec<KeyBinding> {
    vec![
        KeyBinding::new("cmd-n", NewWindow, None),
        KeyBinding::new("cmd-w", HideWindow, None),
        KeyBinding::new("cmd-m", MinimizeWindow, None),
        KeyBinding::new("cmd-q", IgnoreQuit, None),
        KeyBinding::new("cmd-t", NewTerminalTab, None),
        KeyBinding::new("cmd-\\", SplitRight, None),
        KeyBinding::new("cmd--", SplitDown, None),
    ]
}

fn application_menus() -> Vec<Menu> {
    vec![
        Menu::new("Water").items([MenuItem::os_submenu("Services", SystemMenuType::Services)]),
        Menu::new("File").items([
            MenuItem::action("New Window", NewWindow),
            MenuItem::action("New Terminal Tab", NewTerminalTab),
        ]),
        Menu::new("View").items([
            MenuItem::action("Split Right", SplitRight),
            MenuItem::action("Split Down", SplitDown),
        ]),
        Menu::new("Window").items([
            MenuItem::action("Hide Window", HideWindow),
            MenuItem::action("Minimize", MinimizeWindow),
        ]),
    ]
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn custom_titlebar_window_options_hide_the_native_titlebar() {
        let options = water_window_options(Bounds::default());
        assert!(options.app_owns_titlebar_drag);
        assert_eq!(options.window_decorations, Some(WindowDecorations::Client));
        let titlebar = options.titlebar.expect("Water uses an integrated titlebar");
        assert!(titlebar.appears_transparent);
        assert!(titlebar.title.is_none());
    }

    #[test]
    fn menu_bar_has_window_controls_without_a_quit_item() {
        let menus = application_menus();
        assert_eq!(
            menus
                .iter()
                .map(|menu| menu.name.as_ref())
                .collect::<Vec<_>>(),
            ["Water", "File", "View", "Window"]
        );
        assert!(menus.iter().all(|menu| menu.items.iter().all(|item| {
            !matches!(item, MenuItem::Action { name, .. } if name.contains("Quit"))
        })));
    }

    #[gpui::test]
    fn application_shortcuts_route_through_the_focused_window(cx: &mut gpui::TestAppContext) {
        let mut host = crate::app::ModelHost::start();
        let client = host.client();
        let snapshot = client.state_dump().unwrap();
        let application = WaterApplication::new(client, snapshot, AppConfig::default());

        cx.update(|cx| {
            cx.bind_keys(window_key_bindings());
            let application_for_action = application.clone();
            cx.on_action(move |_: &NewWindow, cx| application_for_action.open_window(cx));
            application.open_window(cx);
        });
        let first_window = cx.read(|cx| cx.windows()[0]);
        cx.update(|cx| {
            first_window
                .update(cx, |_, window, _| window.activate_window())
                .unwrap();
        });
        cx.run_until_parked();

        cx.simulate_keystrokes(first_window, "cmd-n");
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);

        let second_window = cx.read(|cx| cx.windows()[1]);
        cx.update(|cx| {
            second_window
                .update(cx, |_, window, _| window.activate_window())
                .unwrap();
        });
        cx.run_until_parked();
        cx.simulate_keystrokes(second_window, "cmd-w");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 1);

        let remaining_window = cx.read(|cx| cx.windows()[0]);
        cx.simulate_keystrokes(remaining_window, "cmd-q");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 1);

        host.shutdown();
    }
}
