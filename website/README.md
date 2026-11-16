# Fennec website

Fennec's landing page, adapted from `runta-dev/errand-website` at
`6d9e5d83f50345684da2114d2ac491589ad43f1c`.

## Develop

```sh
cd website
npm ci
npm run dev -- --port 4186
npm run typecheck
npm run build
```

The vinext static export is written to `dist/client`. The shared landing CSS
(`app/globals.css`) and design tokens (`app/reference-design.css`) retain the
reference template's typography, navigation, spacing, buttons, section layouts,
and responsive rules. `components/demo.css` describes the Herdr window and
playback controls. Agent/fox artwork is copied from this repository's native UI.

The interactive preview is local React state only. It does not attach to real
sessions, run commands, or use model credentials. Its 44-second timeline opens
Codex, Claude Code and Pi in separate Herdr tabs, types sample development tasks,
and runs `fen` to show the live-session picker. Session titles, counts and
Working/Idle groups share the same timeline across both windows.

`FennecPanel.tsx` imports the shipping `native/macos/UI/panel.css` as raw text
inside an isolated iframe. It reuses native component classes for Agents,
Sessions, Governance and Account without loading the native bridge. The sample
installed-agent list includes ChatGPT and two Coming soon entries, matching the
app's discovery model; the public harness list remains the seven cloud CLIs.
Accounts, usage, commands and sessions are fictional. The terminal renderers
are styled illustrations, not recordings or measurements of agent latency.

Playback starts once when visible and pauses offscreen or in a background tab.
Reduced motion disables autoplay and decorative animation. Users can play,
pause, replay, seek, switch panels/tabs, copy attach commands, attach to a sample
session, inspect/filter activity, change the sample permission mode or stop a
sample session. The `fen` picker supports arrows/j/k, Enter, x with confirmation,
r and Escape. Replay restores the initial state. FAQs use native disclosures.

## Publish to Cloudflare

```sh
npm run deploy
```

Uses the authenticated Runta Cloudflare account and a separate `fennec-website`
Worker with Static Assets (`deploy/worker.toml`). It does not modify Errand's
Worker or domains. No server, tokens, database, or API is needed by the public
site. Generated output, dependencies and Wrangler state are ignored by Git.

The Fennec source repository is public. Download links use
`/download/fennec/latest`, which resolves through the existing R2 release route.
Setup instructions and release notes remain linked separately. Fennec Computer
Use has its own page and `/download/computer-use/latest` download; it is a
separate app, not a prerequisite for cloud sessions.

## Landing content sources

- `components/agents.ts` supplies the supported list, FAQ and interactive demo.
  Keep it aligned with cloud-capable entries in `internal/agent/catalog.go`.
- `README.md` describes tmux, cmux, Herdr and native model account support,
  including the OpenCode 2 credential migration limitation.
- `docs/cmux-hooks.md` records the approved cloud Codex hook bridge and its
  verified scope; it does not include the separate cmux-cua MCP integration.
- `docs/native-agent-credentials.md` documents conversation migration for all
  seven supported cloud harnesses (`1c65c37`).
- `74a9098` adds automatic terminal reconnection; `5366603` adds clipboard image
  paste. `docs/predictive-echo.md` covers supported input prediction, including
  Chinese input. Avoid promising a fixed latency or universal prediction.
- `df98d56`, `5df71a3` and `2178b8b` add the standalone Computer Use app and its
  setup-window agent connection flow.
- Recent transport changes remain implementation details. Outstanding WAN
  acceptance limits do not support a zero-latency or uninterrupted-network claim.

## UI acceptance (2026-11-11)

- Compared desktop rendering with the local Errand reference checkout.
- 1440px desktop: document width 1440px; no broken images or missing anchors.
- 390px mobile: document width 390px, no horizontal overflow.
- Verified agent switching, all three workflow steps, copy command, reset, FAQ.
- Verified the site locally before initiating the Cloudflare deployment.

## Workflow demo acceptance (2026-11-16)

- Checked the command typing, prompt typing, launches, session counts/titles and
  Working/Idle transitions throughout the timeline.
- Exercised the four Fennec panels and Herdr tabs, native tab indicator, activity
  filter, permission select, clipboard, session stop, and picker keyboard flow.
- Verified play/pause, offscreen suspension, reduced motion, and replay reset.
- Checked 1440, 1024, 800, 768, 390 and 320 px widths: no horizontal overflow in
  the page or native frame, no broken images, and no browser exceptions.

## Live deployment

- Production URL: https://fennec.runta.com
- Worker URL: https://fennec-website.runta-inc.workers.dev
- Worker version: `f2c6eb2c-9b54-4b90-9c45-758ce715bcf3`
- Custom domains: `fennec.runta.com` (canonical) and `runfennec.com` (alias),
  managed through `deploy/worker.toml`.
- `npm run preview` serves the built static assets through local Wrangler on 4188.
