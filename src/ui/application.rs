use std::cell::RefCell;
use std::path::PathBuf;
use std::rc::Rc;
use std::sync::Arc;

use gpui::{
    App, AppContext, Bounds, Focusable, KeyBinding, Keystroke, Menu, MenuItem, QuitMode,
    SystemMenuType, Task, WeakEntity, WindowBounds, WindowDecorations, WindowHandle, WindowOptions,
    actions, px, size,
};

use crate::app::{CommandClient, ModelSnapshot, ModelSnapshotReceiver};
use crate::config::AppConfig;

use super::WorkspaceView;
#[cfg(feature = "runtime-screenshot")]
use super::control::UiScreenshot;
use super::control::{UiControlReceiver, UiControlRequest, UiKeystrokeResult, UiSnapshot};
use super::settings::SettingsView;

actions!(
    water,
    [
        NewWindow,
        HideWindow,
        MinimizeWindow,
        IgnoreQuit,
        NewTerminalTab,
        NewWorkspace,
        ToggleSidebar,
        RenameWorkspace,
        RenameTab,
        SplitRight,
        SplitDown,
        OpenSettings
    ]
);

#[derive(Clone)]
pub struct WaterApplication {
    state: Rc<WaterApplicationState>,
}

struct WaterApplicationState {
    client: CommandClient,
    config: RefCell<AppConfig>,
    config_path: PathBuf,
    snapshot: RefCell<ModelSnapshot>,
    views: RefCell<Vec<WeakEntity<WorkspaceView>>>,
    settings_window: RefCell<Option<WindowHandle<SettingsView>>>,
}

impl WaterApplication {
    pub fn new(client: CommandClient, snapshot: ModelSnapshot, config: AppConfig) -> Self {
        Self::new_with_config_path(client, snapshot, config, AppConfig::default_load_path())
    }

    pub fn new_with_config_path(
        client: CommandClient,
        snapshot: ModelSnapshot,
        config: AppConfig,
        config_path: PathBuf,
    ) -> Self {
        Self {
            state: Rc::new(WaterApplicationState {
                client,
                config: RefCell::new(config.normalized()),
                config_path,
                snapshot: RefCell::new(snapshot),
                views: RefCell::new(Vec::new()),
                settings_window: RefCell::new(None),
            }),
        }
    }

    pub(crate) fn config(&self) -> AppConfig {
        self.state.config.borrow().clone()
    }

    pub(crate) fn config_path(&self) -> PathBuf {
        self.state.config_path.clone()
    }

    /// Applies the UI-safe portion of a newly saved config immediately. Shell,
    /// startup, and PTY history settings are intentionally left to the next
    /// process start; the settings page reports that restart requirement.
    pub(crate) fn apply_config(&self, config: AppConfig, cx: &mut App) {
        let config = config.normalized();
        self.state.config.replace(config.clone());
        cx.clear_key_bindings();
        cx.bind_keys(configured_window_key_bindings(&config));

        let views = self.state.views.borrow().clone();
        let mut live_views = Vec::with_capacity(views.len());
        for view in views {
            if view
                .update(cx, |workspace, cx| {
                    workspace.apply_config(config.clone(), cx);
                })
                .is_ok()
            {
                live_views.push(view);
            }
        }
        self.state.views.replace(live_views);
    }

    pub fn install(
        &self,
        cx: &mut App,
        snapshot_receiver: ModelSnapshotReceiver,
        ui_control_receiver: UiControlReceiver,
    ) {
        cx.set_quit_mode(QuitMode::Explicit);
        let config = self.config();
        cx.clear_key_bindings();
        cx.bind_keys(configured_window_key_bindings(&config));

        let application = self.clone();
        cx.on_action(move |_: &NewWindow, cx| application.open_window(cx));
        let application = self.clone();
        cx.on_action(move |_: &OpenSettings, cx| application.open_settings(cx));
        let state = self.state.clone();
        let _ = cx.intercept_keystrokes(move |event, _window, cx| {
            let ignore_quit = state.config.borrow().shortcuts.ignore_quit.clone();
            if shortcut_matches_or_default(&ignore_quit, "cmd-q", &event.keystroke) {
                cx.stop_propagation();
            }
        });
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
        let config = self.config();
        let root = cx.new(|cx| {
            WorkspaceView::new_with_config(
                self.state.client.clone(),
                self.state.snapshot.borrow().clone(),
                cx.focus_handle(),
                config.clone(),
            )
        });
        let weak_root = root.downgrade();
        let focus_handle = root.read(cx).focus_handle(cx);
        let bounds = Bounds::centered(
            None,
            size(
                px(config.startup.window_width),
                px(config.startup.window_height),
            ),
            cx,
        );
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

    pub fn open_settings(&self, cx: &mut App) {
        if let Some(window) = *self.state.settings_window.borrow()
            && window.is_active(cx).is_some()
        {
            let _ = window.update(cx, |_, window, _cx| window.activate_window());
            return;
        }

        let root = cx.new(|cx| SettingsView::new(self.clone(), cx.focus_handle()));
        let focus_handle = root.read(cx).focus_handle(cx);
        let bounds = Bounds::centered(None, size(px(980.), px(760.)), cx);
        match cx.open_window(settings_window_options(bounds), move |window, cx| {
            window.activate_window();
            window.focus(&focus_handle, cx);
            root
        }) {
            Ok(window) => {
                self.state.settings_window.replace(Some(window));
            }
            Err(error) => tracing::error!(
                target: "water::ui",
                ?error,
                "failed to open settings window"
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
                    #[cfg(feature = "runtime-screenshot")]
                    UiControlRequest::Screenshot { path, reply } => {
                        let result = cx.update(capture_active_window);
                        let result = match result {
                            Ok(image) => {
                                cx.background_executor()
                                    .spawn(async move { save_screenshot(image, path) })
                                    .await
                            }
                            Err(error) => Err(error),
                        };
                        let _ = reply.send(result);
                    }
                    #[cfg(not(feature = "runtime-screenshot"))]
                    UiControlRequest::Screenshot { path, reply } => {
                        let _ = path;
                        let _ = reply.send(Err(
                            "runtime screenshot support is disabled at compile time".to_owned(),
                        ));
                    }
                }
            }
        })
    }
}

#[cfg(feature = "runtime-screenshot")]
fn capture_active_window(cx: &mut App) -> Result<image::RgbaImage, String> {
    let window = cx
        .active_window()
        .or_else(|| {
            cx.window_stack()
                .and_then(|windows| windows.into_iter().next())
        })
        .or_else(|| cx.windows().into_iter().last())
        .ok_or_else(|| "Water has no open window".to_owned())?;
    window
        .update(cx, |_, window, _cx| {
            window
                .render_to_image()
                .map_err(|error| format!("could not capture Water window: {error}"))
        })
        .map_err(|error| format!("could not access Water window: {error}"))?
}

#[cfg(feature = "runtime-screenshot")]
fn save_screenshot(image: image::RgbaImage, path: PathBuf) -> Result<UiScreenshot, String> {
    let path = if path.extension().is_some() {
        path
    } else {
        path.with_extension("png")
    };
    if let Some(parent) = path.parent()
        && !parent.as_os_str().is_empty()
    {
        std::fs::create_dir_all(parent)
            .map_err(|error| format!("could not create screenshot directory: {error}"))?;
    }
    let width = image.width();
    let height = image.height();
    image
        .save_with_format(&path, image::ImageFormat::Png)
        .map_err(|error| format!("could not save screenshot {}: {error}", path.display()))?;
    Ok(UiScreenshot {
        path: path.display().to_string(),
        width,
        height,
    })
}

fn water_window_options(bounds: Bounds<gpui::Pixels>) -> WindowOptions {
    WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(bounds)),
        // Water renders the complete titlebar, including window controls,
        // inside its views. Do not leave the platform titlebar visible;
        // client decorations are requested for platforms that support them.
        titlebar: None,
        app_owns_titlebar_drag: true,
        window_decorations: Some(WindowDecorations::Client),
        ..Default::default()
    }
}

fn settings_window_options(bounds: Bounds<gpui::Pixels>) -> WindowOptions {
    water_window_options(bounds)
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

#[cfg(test)]
pub(crate) fn window_key_bindings() -> Vec<KeyBinding> {
    configured_window_key_bindings(&AppConfig::default())
}

pub(crate) fn configured_window_key_bindings(config: &AppConfig) -> Vec<KeyBinding> {
    let shortcuts = &config.shortcuts;
    vec![
        safe_key_binding(&shortcuts.open_settings, "cmd-,", OpenSettings),
        safe_key_binding(&shortcuts.new_window, "cmd-n", NewWindow),
        safe_key_binding(&shortcuts.hide_window, "cmd-w", HideWindow),
        safe_key_binding(&shortcuts.minimize_window, "cmd-m", MinimizeWindow),
        safe_key_binding(&shortcuts.ignore_quit, "cmd-q", IgnoreQuit),
        safe_key_binding(&shortcuts.new_terminal_tab, "cmd-t", NewTerminalTab),
        safe_key_binding(&shortcuts.new_workspace, "cmd-shift-n", NewWorkspace),
        safe_key_binding(&shortcuts.toggle_sidebar, "cmd-e", ToggleSidebar),
        safe_key_binding(&shortcuts.rename_workspace, "cmd-shift-e", RenameWorkspace),
        safe_key_binding(&shortcuts.rename_tab, "cmd-shift-t", RenameTab),
        safe_key_binding(&shortcuts.split_right, "cmd-\\", SplitRight),
        safe_key_binding(&shortcuts.split_down, "cmd--", SplitDown),
    ]
}

fn safe_key_binding<A: gpui::Action>(source: &str, fallback: &str, action: A) -> KeyBinding {
    let source = if shortcut_is_valid(source) {
        source.to_owned()
    } else {
        fallback.to_owned()
    };
    KeyBinding::new(&source, action, None)
}

fn shortcut_is_valid(source: &str) -> bool {
    let source = source.trim();
    !source.is_empty()
        && source
            .split_whitespace()
            .all(|keystroke| Keystroke::parse(keystroke).is_ok())
}

pub(crate) fn shortcut_matches(source: &str, actual: &Keystroke) -> bool {
    let Some(source) = source.split_whitespace().next() else {
        return false;
    };
    let Ok(expected) = Keystroke::parse(source) else {
        return false;
    };
    expected.modifiers == actual.modifiers && expected.key == actual.key
}

pub(crate) fn shortcut_matches_or_default(
    source: &str,
    fallback: &str,
    actual: &Keystroke,
) -> bool {
    if shortcut_is_valid(source) {
        shortcut_matches(source, actual)
    } else {
        shortcut_matches(fallback, actual)
    }
}

fn application_menus() -> Vec<Menu> {
    vec![
        Menu::new("Water").items([
            MenuItem::action("Settings…", OpenSettings),
            MenuItem::separator(),
            MenuItem::os_submenu("Services", SystemMenuType::Services),
        ]),
        Menu::new("File").items([
            MenuItem::action("New Window", NewWindow),
            MenuItem::action("New Workspace", NewWorkspace),
            MenuItem::action("New Terminal Tab", NewTerminalTab),
            MenuItem::separator(),
            MenuItem::action("Rename Workspace", RenameWorkspace),
            MenuItem::action("Rename Tab", RenameTab),
        ]),
        Menu::new("View").items([
            MenuItem::action("Toggle Sidebar", ToggleSidebar),
            MenuItem::separator(),
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
        assert!(
            options.titlebar.is_none(),
            "Water owns the complete titlebar"
        );
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

    #[test]
    fn menu_bar_surfaces_workspace_and_sidebar_actions() {
        let menus = application_menus();
        let labels = menus
            .iter()
            .flat_map(|menu| menu.items.iter())
            .filter_map(|item| match item {
                MenuItem::Action { name, .. } => Some(name.as_ref()),
                MenuItem::Separator | MenuItem::Submenu(_) | MenuItem::SystemMenu(_) => None,
            })
            .collect::<Vec<_>>();
        assert!(labels.contains(&"Settings…"));
        assert!(labels.contains(&"New Workspace"));
        assert!(labels.contains(&"Rename Workspace"));
        assert!(labels.contains(&"Rename Tab"));
        assert!(labels.contains(&"Toggle Sidebar"));
    }

    #[gpui::test]
    fn settings_shortcut_opens_one_standalone_settings_window(cx: &mut gpui::TestAppContext) {
        let mut host = crate::app::ModelHost::start();
        let client = host.client();
        let snapshot = client.state_dump().unwrap();
        let application = WaterApplication::new(client, snapshot, AppConfig::default());

        cx.update(|cx| {
            cx.bind_keys(configured_window_key_bindings(&AppConfig::default()));
            let application_for_action = application.clone();
            cx.on_action(move |_: &OpenSettings, cx| application_for_action.open_settings(cx));
            application.open_window(cx);
        });
        let workspace_window = cx.read(|cx| cx.windows()[0]);
        cx.simulate_keystrokes(workspace_window, "cmd-,");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);
        assert!(cx.read(|cx| {
            cx.windows()
                .iter()
                .any(|window| window.root_entity_type_name().contains("SettingsView"))
        }));

        cx.simulate_keystrokes(workspace_window, "cmd-,");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);
        host.shutdown();
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
