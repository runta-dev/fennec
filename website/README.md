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
and responsive rules. Agent artwork comes from the native UI. Go is required to
export the native demo assets before development and production builds.

The walkthrough replays real Codex, Claude Code, Pi and fen PTY output using
the shipping xterm renderer. The Fennec panel loads the original native HTML,
CSS, scripts and custom menus with isolated browser-only sample data. It never
connects to an account or daemon. See [recording workflow and provenance](demo/README.md).

Playback loops while visible and pauses offscreen or in a background tab.
Reduced motion shows the completed scene. Clicking terminal tabs or native
controls pauses automatic navigation for eight seconds, then resumes. Every
cycle resets terminal tabs, sessions and native fixture state. Space on the focused walkthrough toggles
playback, and Escape pauses. The terminal is a recording; native controls operate
on fixtures. The original walkthrough remains directly visible on the homepage.

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

## Verification

Run typecheck, the static build, the source-language and localization checks,
and the recorder byte-preservation tests. Ensure
account and cost disclosures appear beside the main CTA and in setup before the
FAQ. Check responsive layouts from 320 to 1440 px and the primary mobile nav.
In the walkthrough, check the actual native menus, all four panels, terminal
playback, and automatic session updates. Check the Kimi icon in light and dark
UI themes; its blue accent must remain blue.

## Hosting

Production domains are https://fennec.runta.com (canonical) and
https://runfennec.com (alias), managed by `deploy/worker.toml`.
`npm run preview` serves built assets through local Wrangler on port 4188.
