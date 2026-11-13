# Browser set: proposal

Status: proposal only. Nothing here is built yet; the golden image has no Chromium.

## What macOSWorld offers

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

## Proposal: our own local-page tasks

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
