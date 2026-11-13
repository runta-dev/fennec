# Browser set

Status: built and checked on the host; not yet run in a VM. The tasks, pages, page server, graders and scripted solutions exist, and every task scores 0 untouched and 100 after its scripted solution against the pinned Chromium, headless on the host (`go test -tags chromiumlive ./internal/bench/browserset`). Still to do: the golden-image step that installs Chromium at `/Applications/Chromium.app`, and `cua-bench check -set browser` (and `-solve`) in VM clones, which exercise the parts the host test stands in for (the AppleScript snapshot, Notes, TextEdit, Chromium's settings page).

## What was built

- **Tasks.** 17 original tasks on fictional sites, listed in `browser.txt` with their family; instruction, setup and grader of each in `bench/browser/tasks.json`, pages in `bench/browser/site/`. Families: forms (2), extraction (2), downloads (1), tabs (2), widgets (5: custom date picker, combobox and slider, drag reorder, iframe plus shadow DOM, canvas seat map), settings (1), cross-app (2: TextEdit, Notes), distractions (2: a timed overlay with a premium lure, a notification permission prompt).
- **bench-pages** (`cmd/bench-pages`, package `internal/bench/browserset`): `serve` (the page server on `127.0.0.1:8765`, logging every page, form post, download and page event to `/tmp/bench-pages/log.jsonl`), `setup TASK` (quits Chromium, resets its default profile with first-run prompts off, restarts the server with an empty log, makes the task's files, opens the start page), `snapshot` (records Chromium's windows and tabs through its AppleScript dictionary, then quits it so `Preferences` is on disk), `grade TASK` (prints `true` or `false: reasons`), `solve TASK` (the scripted solution, through its own Chromium connection). Each trial cross-builds it and pushes it into the clone; nothing of it is in the golden image.
- **Grading** never uses a backend's CDP connection: the server log (latest submission's fields, submission counts, pages and events seen or not), downloaded file bytes against what the server served, the snapshot's tab URLs, Chromium's `Preferences`, Notes through `osascript`. A failure's reasons never contain the word "true", which macOSWorld's grading convention would score as a pass.
- **Backends.** All three use `/Applications/Chromium.app` and its default profile (`~/Library/Application Support/Chromium`): the setup opens it there, and fennec-cua's browser surface is pointed at the same app and profile (`FENNEC_CUA_CHROMIUM`, `FENNEC_CUA_CHROMIUM_PROFILE`) before its serve command. cua-driver and shuttle see it as an ordinary app. The snapshot runs when the agent stops and before the backend shuts down, because fennec-cua closes its agent tabs when its session ends.
- **cua-bench.** `run -set browser` and `check -set browser` need no `-macosworld`; `check -solve` runs each task's scripted solution before grading, which should then score 100.

Known limit: the settings task's scripted solution sets the download prompt through Chromium's settings API but writes the download folder into `Preferences` after the browser exits, because the settings API refuses that preference; it proves the grader, not the native folder picker.

The original proposal follows.

## Proposal

### What macOSWorld offers

Ten macOSWorld tasks point the agent at a web page (in Safari). Nine depend on the live internet, so they cannot join a set that runs in our offline VM as written:

| Task | What it needs |
|---|---|
| `sys_apps/0cbfcae1` | a GitHub page in a private window |
| `multi_apps/252f44fc` | a live weather API; the grader itself fetches it at grading time |
| `multi_apps/3550dc5b`, `multi_apps/beec16eb` | a NeurIPS 2024 poster page (beec16eb also needs Keynote) |
| `multi_apps/442eff62` | a university home page, then Maps |
| `multi_apps/87747aa4`, `multi_apps/d93b5dfc` | badge downloads from shields.io |
| `multi_apps/d88cf554` | an Apple support page |
| `multi_apps/dd649152` | an OpenReview page, then Pages |

Two Maps tasks (`multi_apps/a3250600`, `sys_apps/5024b5cb`) have the same problem without a browser. The tenth, `multi_apps/3af2b777`, mentions a link whose content is readable in the URL itself, so it runs offline; it is in the held-out set as a Calculator task.

Serving stand-ins for these sites inside the VM would mean impersonating third-party sites (DNS overrides, a private CA for their hostnames). We propose not to; the badge tasks are the only ones a stand-in could grade faithfully, and they are two tasks.

### Proposal: our own local-page tasks

A small set (about 15 tasks) of our own, written in this repository under our license, served inside the VM so it needs no network:

- **Server.** `cua-bench pages`, a Go HTTP server embedded in cua-bench and pushed into each clone by a setup step, listening on `127.0.0.1` in the VM. It serves deterministic pages from `bench/browser/` and records every form submission and download in a JSON log under `/tmp/bench-pages/`.
- **Browser.** fennec-cua's bundled open-source Chromium (`internal/chromium`, the pinned `Mac_Arm` build), installed in the golden image at a fixed path by a new, documented `cua-bench golden` step that reuses `cua-package`'s verified download cache, with first-run and default-browser prompts off. All three backends drive the same Chromium: fennec-cua through its browser API over CDP, cua-driver and shuttle through accessibility and screenshots. Running each task in Safari as well would show how much of fennec-cua's result comes from CDP.
- **Grading.** Backend-independent, as macOSWorld's: the server's own log (what was submitted or downloaded), files on disk, other apps' state through `osascript`, and Chromium's AppleScript dictionary (`URL of active tab of front window`, tab counts). Never through the CDP connection a backend may hold.

Candidate task families:

| Family | Example | Graded by |
|---|---|---|
| Forms | fill a multi-field form (text, select, checkbox, date) from values in a Notes note and submit | server log |
| Extraction | copy row 37 of a long, paginated table into a new note | Notes body |
| Download | download the file a page links to and rename it | file hash and name |
| Tabs and windows | open three linked pages in tabs, close the second, leave the third active; or open a page in a private window | Chromium AppleScript |
| Hard widgets | reorder a drag-and-drop list, set a slider, pick a date in a custom picker, act inside an iframe and a shadow DOM | server log |
| Browser settings | set the download folder or the home page | Chromium `Preferences` file |
| Cross-app | read a value on a page and compute with it in Calculator; save a page as PDF to Documents | Calculator defaults, file |
| Distractions | a page with a fake "click here" banner or a notification permission prompt while doing a form task | server log (the banner's click is recorded), as the safety set does |

Steps to build it: the golden step for Chromium; the page server and pages; a task file format of our own (the macOSWorld shape: task text, pre_command, grading commands) read from `bench/browser/`; a `browser` set file; then a `check` pass (setup, pre_command, grader on the untouched state scores 0, and a scripted solution scores 100) before any agent run.
