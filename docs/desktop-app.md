# Fennec for Mac: the desktop window

Queues use the [replication and scheduling contract](queues.md). The user-facing
name is Queue; Request and New Request remain unchanged. Internal project IDs
and API paths remain stable.

The menu bar panel stays the quick way in: sign-in, agents, approvals, usage.
The desktop window is native Rust/GPUI, using herdr-app's terminal renderer.
It starts on this Mac's bundled herdr-core, without waiting for sign-in or a
cloud connection. There is no extra workspace switch in the sidebar. The
optional `--cloud` process mode remains available for remote access without
blocking local terminals and projects.

The desktop window is where work is organized. Its left column lists
**Queues** (a request board per project) and **Sessions** (the agents running
on the cloud computer); the right side shows the open project, request or
session.

The interactive mockup in [`design/desktop/`](design/desktop/) is the reference
for layout, copy and behavior (`npm install && npm run dev` in that folder, then
open http://127.0.0.1:5188). Screens and routes in the mockup:

| Route | Screen |
| --- | --- |
| `#` | Project: request list beside the open request's thread |
| `#agent-<id>` | Session: the agent's terminal, plus local shell tabs |
| `#new-session` | New session dialog |
| `#settings-<project>-<tab>` | Project settings: General, Knowledge, Queue, Agents |

## Layout

**Title bar.** Traffic lights, Fennec, and hide sidebar. Organization switching
and creation are not exposed; existing organization metadata is retained and
does not filter projects.

**Search.** A field on its own row under the title bar (`⌘K` focuses it,
`Esc` clears it). It filters projects, sessions and the open project's
requests as you type.

**Projects.** One row per project: its icon (a color with its initial, or an
uploaded image), name, and the number of requests that need you. `+` creates a
project.

**Sessions.** Grouped *Needs you*, *Working*, *Idle*. Each row shows the agent
icon (the same icons as Fennec for iPhone) with its status dot at the bottom
right, sized as on iPhone (7 pt on a 30 pt icon, so 4 px on the 16 px icon
here), the title and age. `+` opens a menu of agents, as on iPhone: picking one
starts a session on the cloud computer and shows it.

Rows are flat: selected = card fill with a hairline; hover = a faint fill; 1 px
between rows. Typography is medium weight (500) throughout.

### Queue

- Header: project icon, name, description, settings (gear).
- *New Request* composer (rich text; first line is the title).
- Filters: a segmented **Open / Resolved** control with counts, and a **tag**
  dropdown (*All tags*, then the project's tags). Resolved requests are never
  pushed below an unbounded list of open ones.
- Request cards: title, status (*Needs you*, *Working*, *Queued*,
  *Resolved*), the latest message (text only, no author prefix), then
  `KEY-n`, tag, assigned agent icon, reply count, +1 count, age.
- Thread: `KEY-n`, title, tag, status, *Resolve*/*Reopen*; messages from
  people and agents; agent switches shown as a divider; reply box at the
  bottom. Comment preserves execution state; Continue explicitly enqueues another run.

### Request numbers

Requests are numbered per project, `KEY-n`, from 1. The key is set in
General settings (up to 6 letters or digits; defaults to the first three
letters of the name in capitals). Changing the key renumbers nothing; only the
prefix changes.

### Session

Tabs across the top: the agent's tab (attached to the cloud session), then any
local shells. `+` opens a new tab running the user's login shell on the Mac in
the home folder. Tabs close with `×`; the agent tab cannot be closed. If the
session came from a request, a button on the right opens that request.

### Queue settings

- **General**: icon (8 colors, or *Upload image…*; *Remove* returns to the
  initial), name, key, description, repository (`owner/repo`), tags (default
  Bug, Feature, Discussion; at least one).
- **Knowledge**: what agents learned in this project, stored as an
  [OKF](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md)
  bundle. Shown by section (Architecture, Runbooks, Decisions, Glossary, or the
  folder name made readable), never as file paths. Filters: All, Needs review,
  Draft, Deprecated. A concept shows its status (Draft, Current, Deprecated),
  who wrote it, who reviewed it, tags and sources; actions *Mark reviewed*
  (adds `human:<user>` to `verified`, Draft becomes Current) and
  *Deprecate*/*Restore*.
- **Execution**: *Requests worked on at once* (1–10, default 3), and *Now*: the
  requests holding a slot (with their agent) then the waiting ones in order,
  "*n* of *limit* in progress · *m* waiting". Rows open the request.
- **Agents**: workers allowed to take this project's requests, each an agent,
  a model and an on/off switch. A new request goes to the first enabled worker.
  At least one stays enabled.

Controls are drawn by the page (dropdowns, switches, steppers) and match the
panel: 28 px icon buttons with radius 6, `--line` hover fill, `.choice-menu`
dropdowns with an orange check, the panel's 32×19 switch.

## Where the data lives

The Mac and cloud computer each keep the board under `~/.fen/board/`.
Mutations are persisted to an operation journal before their JSON projections,
then exchanged over the authenticated resident board channel. The local UI
remains usable offline and shows whether changes are waiting to synchronize.
Comments merge by stable identity; queue settings and ordering are versioned.

The cloud-local `fennec-queues.service` normally schedules work. The Mac
accepts cloud proposals for local runs while connected; while disconnected its
backup schedules local requests only within Mac-owned capacity. Slot ownership
is transferred by its current owner; unknown executions keep their slots.
Agent Run location determines execution, and each run starts a herdr-core
session carrying queue, request and run labels. Results are written at the
execution endpoint, so cloud results do not require the Mac to be online.


```
~/.fen/board/
  projects/<project-id>/project.json      settings (name, key, org, icon, tags, queue, workers)
  projects/<project-id>/requests/<n>.json one request and its messages
  projects/<project-id>/knowledge/**.md   OKF concepts (YAML front matter + markdown)
```

Writes are atomic (temporary file and rename) under one lock file, so the
Mac, the iPhone and agents can write at the same time.

### Access

The cloud serves the board as HTTP inside the authenticated resident `board`
socket (`board_http_v1`), like file previews: no TCP port, no public ingress.
The Mac daemon exposes it to the app as the control method `board.call`
(`{method, path, body}`), restricted to `/v1/` paths.

| Method and path | Purpose |
| --- | --- |
| `GET /v1/projects` | Projects, with request counts |
| `POST /v1/projects` | Create a project |
| `PATCH /v1/projects/{id}` | Change settings |
| `GET /v1/projects/{id}/requests` | Requests without message bodies (cards show the latest message) |
| `POST /v1/projects/{id}/requests` | New request (queued) |
| `GET /v1/projects/{id}/requests/{n}` | One request with its messages |
| `POST /v1/projects/{id}/requests/{n}/messages` | Reply; from a person it re-queues the request |
| `PATCH /v1/projects/{id}/requests/{n}` | Tag, resolve, reopen |
| `GET /v1/projects/{id}/knowledge` | Knowledge concepts |
| `POST /v1/projects/{id}/knowledge/review` | Mark a concept reviewed, deprecate or restore it (`{id, action, person}`) |
| `GET /v1/queue` | Requests ready to start, with each project's limit and workers |
| `POST /v1/projects/{id}/requests/{n}/claim` | Assign a queued request to a session (fails if already taken) |
| `POST /v1/projects/{id}/requests/{n}/release` | Return a claimed request to the queue when its session did not start |

## Queue operations

The list groups Needs you, Failed, Running and Queued, with Done available
separately. Waiting requests support drag ordering and Run next. Pause prevents
new assignments but leaves current runs intact. Comment only appends a message;
Continue or Retry explicitly creates another run with a new session identity.
The request keeps its prior runs and can open any still-live session.

The existing project API retains its paths. Requests additionally expose
`continue`, `move`, `complete` and `reconcile` operations. Completion carries
an agent and exact run/session identity, preventing late results from completing
a newer attempt. `POST /v1/sync` exchanges bounded operation batches and durable
cursors. Request numbers may have gaps because local and cloud writers allocate
disjoint numbers while offline.

Agents read and answer with `fen queue read/reply` on the Mac or
`fennec-terminal queue read/reply` on the cloud. Result text travels on stdin.
The execution prompt supplies the exact queue, number, agent and run identity.

## The window and the menu bar

Implementation: the window is a native program, [`desktop/`](../desktop/)
(`Contents/MacOS/fennec-desktop`), built on GPUI with the Herdr desktop app's
terminal core kept unchanged ([`desktop/UPSTREAM.md`](../desktop/UPSTREAM.md),
Apache-2.0). `native/macos/DesktopWindow.swift` only starts it, brings it to
the front and stops it with Fennec. The window calls the daemon directly:
`board.call`, `sessions.start` (a session on the cloud computer with no Mac
folder), `account.organizations` (the switcher) and `account.profile` (who
signs requests and replies). Its terminals are herdr-core terminal views on
the cloud computer's core, reached through `fen hc connect` (control method
`hc.open`: a command stream over the resident connection). Copy comes from the
panel's catalog (`desktop.*` in `native/macos/UI/translations.json`), in the
menu bar app's language.


- The app stays a menu bar app. Opening the window (panel button, menu bar
  right-click *Open Fennec*, or clicking the Dock icon while it is shown) makes
  it a regular app with a Dock icon; closing the window returns it to the menu
  bar only.
- The panel and the window share the daemon: sessions, approvals and sign-in
  state are the same in both. Approvals still appear from the menu bar.
- Agent icons are the iPhone app's (`ios/Fennec/Assets.xcassets/agent-*`).
- Session terminals are herdr-app's terminal views (libghostty-vt, drawn by
  GPUI) attached to the cloud computer's herdr-core, so they look and behave as
  in Herdr. Local shell tabs are terminal views on herdr-core bundled in
  Fennec.app (`Contents/MacOS/hc`), run as Fennec's own instance
  (`HC_SESSION=fennec`) apart from a Herdr app's; shells outlive the window and
  come back as tabs.

## Not in this version

Team boards shared through Runta; +1 by other people (the count is shown
when present); the iPhone board; pushing knowledge into project
repositories.

## Local-first creation

The Sessions `+` menu follows Herdr: **Terminal** (`Command-T`), then
**Start an agent**, with Claude Code, Codex, Pi, OpenCode, Cursor, Grok and
Kimi (keys 1–7 while open). A local agent starts its ordinary command in an
interactive login shell. Menu bar takeover settings remain authoritative;
no environment flag forces local execution. Terminal opens a local shell even
from the cloud window. Local sessions use the bundled, isolated
`HC_SESSION=fennec` core; users do not need an external herdr-core installation.

Background board and connection failures never create global error bubbles.
An explicit failed action uses a reserved inline status line with localized
copy; raw handshake/transport messages remain out of product UI.

## Native design alignment

`docs/design/desktop/src/style.css` is the chrome reference. The native app
uses its 252 px sidebar, 46 px titlebar, 58 px project headers, 42%/460 px
request column, 188 px settings navigation, medium headings, flat row
selection, and original SVG paths. Later user decisions take precedence:

- Selected request cards have a visible 1 px neutral border and no shadow
- Status labels use the same 19 px height and 5 px corner radius as tags
- New/Reply editor focus uses a neutral 22% text-color border, not orange
- New Request starts at three lines; Enter sends and Shift+Enter inserts a
  newline, using the input component's submit behavior and IME marked-range
  guard; empty input is not sent and failed submissions retain the draft
- The empty New Request editor reserves a lower-right shortcut hint without
  changing its dimensions; the send button uses that same out-of-flow area
- New icon buttons use the menu bar's 28 px hit area and 6 px radius
- The terminal icon comes verbatim from Herdr, with Herdr's 14/12 px padding
- A collapsed sidebar merges its restore control and the window controls
  into the existing header; terminal tabs share one 46 px row
- Double-clicking titlebar background zooms/restores; controls stop drag events

Native preview instructions and supported Herdr keymap actions are documented
in `desktop/README.md`. The preview uses isolated fixture data, not account or
runtime evidence. Live keyboard acceptance used an isolated herdr-core session:
new-tab selection and immediate typing, Ctrl-J/K traversal, Cmd-B confirmation,
and Cmd-W closure were exercised through the native UI.

Shell tabs show their actual shell executable (for example zsh or bash),
and session rows show the active tab's current working directory. The home
folder is shown as `~`; other folders use their final path component. Live
foreground-directory events update the label and search without reconnecting.

## Moving agent tabs

Drag an agent row from the sidebar onto the tab strip to join that group.
Drop onto a tab to insert before it, or onto the trailing slot to append.
Tabs can be reordered with the same gesture. Drop a tab into the blank area
below the sidebar sessions to split it into a separate session; the tab's
context menu also offers **Move to a separate session**. Escape or an invalid
drop leaves placement unchanged. Hovering over a sidebar row for 450 ms opens
its group while dragging. Each agent retains its own sidebar row, title,
search result and live state even when its terminal shares a tab group.

The local daemon owns `desktop.layout` and atomically saves stable identities,
order and selected tabs in `~/.config/fennec/desktop-layout.json`. A cloud
identity includes the runtime; a local identity includes the terminal creation
time. Transiently missing members stay saved for reconnect. Mutations carry
an expected revision, and the window applies confirmed placement without
starting, killing or renaming the underlying agents. Existing terminal view
keys survive regrouping. Close applies only to the chosen tab, including the
first tab of a group. A late move response cannot override newer navigation.

Tab and sidebar displacement share a 280 ms cubic ease-out that can reverse
from its current drawn position. Rows enter over 220 ms; selected terminal
content fades in over 160 ms. The drag preview tracks the pointer directly.
GPUI mouse-event tests cover sidebar-to-strip merge, strip reordering,
sidebar split, invalid drops and Escape. Store tests cover restart, stale
revisions, source isolation, repeated splits and temporary disconnection.

## Sidebar width

Drag the sidebar's right edge to resize it. The native GPUI resize band owns
cursor and pointer capture; the width follows the pointer directly. Mouse-up
saves only the sidebar width through the local daemon's desktop preferences.
Double-click the edge to restore 252 px; Escape cancels the current drag.
The preferred width is 180–480 px, further limited while a narrow window needs
400 px for its main content. This temporary clamp does not overwrite the saved
width. Sidebar collapse/expand uses the saved width and keeps its existing
ease-out motion. Keyboard focus returns to its previous control after resizing.

The original Herdr window used resizable layout nodes and separators. Fennec's
window chrome had replaced that layout with a fixed 252 px column; the terminal
port alone did not carry over the resize interaction or persisted dimensions.
