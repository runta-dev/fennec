# Fennec for Mac: the desktop window

The menu bar panel stays the quick way in: sign-in, agents, approvals, usage.
The desktop window is where work is organized. Its left column lists
**Projects** (a request board per project) and **Sessions** (the agents running
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

**Title bar.** Traffic lights, then the organization switcher (organization
mark, name, chevron). Its menu lists the user's Runta organizations with a
check on the current one, then *New organization…* (opens the Runta
dashboard). Switching organizations shows that organization's projects.
Sessions belong to the user's cloud computer and are not filtered.
On the right: hide sidebar.

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

### Project

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
  bottom. A reply re-queues the request for its agent.

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

### Project settings

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
- **Queue**: *Requests worked on at once* (1–10, default 3), and *Now*: the
  requests holding a slot (with their agent) then the waiting ones in order,
  "*n* of *limit* in progress · *m* waiting". Rows open the request.
- **Agents**: workers allowed to take this project's requests, each an agent,
  a model and an on/off switch. A new request goes to the first enabled worker.
  At least one stays enabled.

Controls are drawn by the page (dropdowns, switches, steppers) and match the
panel: 28 px icon buttons with radius 6, `--line` hover fill, `.choice-menu`
dropdowns with an orange check, the panel's 32×19 switch.

## Where the data lives

Projects, requests and knowledge are stored **on the user's cloud computer**,
under `~/.fen/board/`. Agents there read and reply to requests directly, and
the iPhone app can show them later over the same channel. Data is one user's;
sharing a board with teammates needs a Runta backend and is not part of this
version.

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

## The queue

The Mac daemon dispatches. Every few seconds while it is signed in and
connected it reads `GET /v1/queue`. For each project with fewer *Working*
requests than its limit it takes the oldest *Queued* request, claims it, and
starts a session on the cloud computer the way a session started from the
iPhone is started (no Mac folder, nothing uploaded; the receipt is saved so
the session shows in Fennec and `fen attach` reaches it), with
the first enabled worker's agent and model and a first message that carries
the request. If the request already has a live session (a reply to a request
the agent worked on), the new messages are sent to that session instead.
When the Mac is off, requests wait as *Queued*.

## What agents do

The first message tells the agent the request (`KEY-n`, title, thread),
the project's repository, and how to answer:

```sh
~/.local/fennec/bin/fennec-terminal reply FEN-42 "Fixed: narrow screens no longer scroll sideways."   # status becomes Needs you
~/.local/fennec/bin/fennec-terminal request FEN-42                                                     # read the thread
```

Agents record what they learn as OKF concepts in the project's knowledge
folder (`type` required; `generated.by` set to the agent; `status: draft`).
People review them in Knowledge.

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
  in Herdr. Local shell tabs are not in this version.

## Not in this version

Team boards shared through Runta; +1 by other people (the count is shown
when present); the iPhone board; pushing knowledge into project
repositories.
