//! Sidebar and tab context menus, using Herdr's compact menu metrics.
use super::*;
use crate::i18n::tf;

#[derive(Clone)]
pub(super) enum Target {
    Project(String),
    Terminal { session: String, tab: String },
}
#[derive(Clone)]
enum Action {
    DeleteProject(String),
    RenameProject(String),
    ProjectSettings(String),
    OpenTerminal(String, String),
    NewTab(String),
    CopyDirectory(String),
    CloseTab(String),
    SplitTab(String),
}
struct Item {
    label: &'static str,
    icon: &'static str,
    action: Action,
}
pub(super) struct ContextMenu {
    position: Point<Pixels>,
    items: Vec<Item>,
    selected: Option<usize>,
}

impl FennecWindow {
    pub(super) fn context_row(
        &self,
        row: Stateful<Div>,
        target: Target,
        cx: &Context<Self>,
    ) -> Stateful<Div> {
        let control_target = target.clone();
        row.on_mouse_down(
            MouseButton::Right,
            cx.listener(move |this, event: &MouseDownEvent, window, cx| {
                cx.stop_propagation();
                this.open_context_menu(target.clone(), event.position, window, cx);
            }),
        )
        .on_mouse_down(
            MouseButton::Left,
            cx.listener(move |this, event: &MouseDownEvent, window, cx| {
                if event.modifiers.control {
                    cx.stop_propagation();
                    this.open_context_menu(control_target.clone(), event.position, window, cx);
                }
            }),
        )
    }

    fn open_context_menu(
        &mut self,
        target: Target,
        position: Point<Pixels>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let items = match target {
            Target::Project(id) => vec![
                Item {
                    label: "context_rename",
                    icon: "context-rename",
                    action: Action::RenameProject(id.clone()),
                },
                Item {
                    label: "project_settings",
                    icon: "gear",
                    action: Action::ProjectSettings(id.clone()),
                },
                Item {
                    label: "delete_queue",
                    icon: "trash",
                    action: Action::DeleteProject(id),
                },
            ],
            Target::Terminal { session, tab } => {
                let mut items = vec![Item {
                    label: "context_open_terminal",
                    icon: "term",
                    action: Action::OpenTerminal(session.clone(), tab.clone()),
                }];
                if crate::core_bin::local().is_some() || crate::preview::active() {
                    items.push(Item {
                        label: "context_new_tab",
                        icon: "plus",
                        action: Action::NewTab(session.clone()),
                    });
                }
                let address = tab.strip_prefix("local:").unwrap_or(&tab);
                if self.group_tabs(&session).len()>1 {
                    items.push(Item{label:"tabs_split",icon:"layers",action:Action::SplitTab(tab.clone())});
                }
                let directory = self
                    .directories
                    .get(address)
                    .cloned()
                    .or_else(|| {
                        self.sessions
                            .iter()
                            .find(|s| s.address == address)
                            .map(|s| s.directory.clone())
                    })
                    .unwrap_or_default();
                if !directory.is_empty() {
                    items.push(Item {
                        label: "context_copy_directory",
                        icon: "link",
                        action: Action::CopyDirectory(directory),
                    });
                }
                items.push(Item {
                    label: "close_tab",
                    icon: "close",
                    action: Action::CloseTab(tab),
                });
                items
            }
        };
        self.menu = None;
        self.pending_focus = None;
        self.context_menu = Some(ContextMenu {
            position,
            items,
            selected: None,
        });
        self.focus.focus(window, cx);
        cx.notify();
    }

    fn dismiss_context_menu(&mut self, restore_focus: bool, cx: &mut Context<Self>) {
        self.context_menu = None;
        if restore_focus {
            if let Some(Route::Session(id)) = &self.route {
                self.pending_focus = Some(self.shown_tab(id));
            }
        }
        cx.notify();
    }

    fn choose_context_item(&mut self, index: usize, window: &mut Window, cx: &mut Context<Self>) {
        let action = self
            .context_menu
            .as_ref()
            .and_then(|menu| menu.items.get(index))
            .map(|item| item.action.clone());
        self.dismiss_context_menu(true, cx);
        match action {
            Some(Action::DeleteProject(id)) => self.delete_queue(id, window, cx),
            Some(Action::RenameProject(id)) => {
                self.go(
                    Route::Settings(id, crate::project_settings::Tab::General),
                    window,
                    cx,
                );
                self.focus_project_name(window, cx);
            }
            Some(Action::ProjectSettings(id)) => self.go(
                Route::Settings(id, crate::project_settings::Tab::General),
                window,
                cx,
            ),
            Some(Action::OpenTerminal(session, tab)) => {
                self.set_shown_tab(session.clone(), tab, cx);
                self.go(Route::Session(session), window, cx);
            }
            Some(Action::NewTab(id)) => {
                if let Some(session) = self.sessions.iter().find(|s| s.address == id).cloned() {
                    self.go(Route::Session(id), window, cx);
                    self.new_shell(session, window, cx);
                }
            }
            Some(Action::CopyDirectory(path)) => {
                cx.write_to_clipboard(ClipboardItem::new_string(path))
            }
            Some(Action::CloseTab(key)) if !crate::preview::active() => {
                self.close_tab_key(key, window, cx)
            }
            Some(Action::SplitTab(key)) => {
                let drag=self.drag_tab(&key);
                self.move_tab(&drag,String::new(),String::new(),cx);
            }
            _ => {}
        }
    }

    fn delete_queue(&mut self, id: String, window: &mut Window, cx: &mut Context<Self>) {
        if crate::preview::active() {
            return;
        }
        let Some(project) = self.projects.iter().find(|p| p.id == id) else {
            return;
        };
        let title = tf("delete_queue_title", &[("name", &project.name)]);
        let answer = window.prompt(
            PromptLevel::Warning,
            &title,
            Some(&t("delete_queue_detail")),
            &[t("cancel").as_str(), t("delete_queue").as_str()],
            cx,
        );
        self._tasks.push(cx.spawn_in(window, async move |this, cx| {
            if answer.await.ok() != Some(1) { return; }
            let deleted_id = id.clone();
            let result = cx.background_spawn(async move {
                board::call("DELETE", &format!("/v1/projects/{deleted_id}"), None)
            }).await;
            let _ = this.update_in(cx, |this, window, cx| match result {
                Ok(_) => {
                    this.deleted_projects.insert(id.clone());
                    this.projects.retain(|p| p.id != id);
                    this.requests.remove(&id);
                    if matches!(&this.route, Some(Route::Project(p) | Route::Settings(p, _)) if p == &id) {
                        this.route = None;
                        this.open = None;
                        this.opening = None;
                        this.thread = None;
                        this.settings = Default::default();
                    }
                    this.refresh_now(window, cx);
                    cx.notify();
                }
                Err(_) => this.say(t("delete_queue_failed"), cx),
            });
        }));
    }

    pub(super) fn context_key(
        &mut self,
        key: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        let Some(menu) = &mut self.context_menu else {
            return false;
        };
        let count = menu.items.len();
        match key {
            "down" => menu.selected = Some(menu.selected.map_or(0, |i| (i + 1) % count)),
            "up" => {
                menu.selected = Some(menu.selected.map_or(count - 1, |i| (i + count - 1) % count))
            }
            "enter" => {
                let selected = menu.selected.unwrap_or(0);
                self.choose_context_item(selected, window, cx);
            }
            "escape" => self.dismiss_context_menu(true, cx),
            _ => {}
        }
        cx.stop_propagation();
        cx.notify();
        true
    }

    pub(super) fn context_menu_view(
        &self,
        window: &Window,
        cx: &Context<Self>,
    ) -> Option<AnyElement> {
        let menu = self.context_menu.as_ref()?;
        let height = px(12.0
            + menu.items.len() as f32 * 31.0
            + if menu
                .items
                .iter()
                .any(|i| matches!(i.action, Action::CloseTab(_) | Action::DeleteProject(_)))
            {
                9.0
            } else {
                0.0
            });
        let viewport = window.viewport_size();
        let position = point(
            menu.position
                .x
                .min((viewport.width - px(238.0)).max(px(8.0))),
            menu.position
                .y
                .min((viewport.height - height - px(8.0)).max(px(8.0))),
        );
        Some(
            v_flex()
                .id("sidebar-context-menu")
                .absolute()
                .left(position.x)
                .top(position.y)
                .w(px(230.0))
                .p(px(5.0))
                .rounded(px(9.0))
                .bg(c("popover"))
                .border_1()
                .border_color(c("border"))
                .occlude()
                .on_mouse_down_out(
                    cx.listener(|this, _, _, cx| this.dismiss_context_menu(false, cx)),
                )
                .children(menu.items.iter().enumerate().map(|(index, item)| {
                    v_flex()
                        .when(
                            matches!(item.action, Action::CloseTab(_) | Action::DeleteProject(_)),
                            |d| d.child(div().mx(px(7.0)).my(px(4.0)).h(px(1.0)).bg(c("border"))),
                        )
                        .child(
                            h_flex()
                                .id(SharedString::from(format!("context-item-{index}")))
                                .h(px(31.0))
                                .px(px(8.0))
                                .gap(px(9.0))
                                .rounded(px(7.0))
                                .text_size(px(12.8))
                                .font_weight(FontWeight::MEDIUM)
                                .text_color(c(if matches!(item.action, Action::DeleteProject(_)) {
                                    "blocked"
                                } else {
                                    "foreground"
                                }))
                                .when(menu.selected == Some(index), |d| d.bg(c("selected")))
                                .hover(|s| s.bg(c("hover")))
                                .on_mouse_down(
                                    MouseButton::Left,
                                    cx.listener(move |this, _, window, cx| {
                                        cx.stop_propagation();
                                        this.choose_context_item(index, window, cx);
                                    }),
                                )
                                .child(ui::icon(
                                    item.icon,
                                    14.0,
                                    c(if matches!(item.action, Action::DeleteProject(_)) {
                                        "blocked"
                                    } else {
                                        "muted"
                                    }),
                                ))
                                .child(t(item.label)),
                        )
                }))
                .into_any_element(),
        )
    }
}
