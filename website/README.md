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
and responsive rules. `app/fennec.css` supplies the Fennec-specific companion and
terminal preview. Agent/fox artwork is copied from this repository's native UI.

The interactive preview is local React state only. It does not attach to real
sessions, run commands, or use model credentials. It supports agent selection,
three workflow stages, session view, copy attach command, and reset. FAQs use
native disclosure controls. Reduced-motion settings disable animation.

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

## Live deployment

- Production URL: https://fennec.runta.com
- Worker URL: https://fennec-website.runta-inc.workers.dev
- Worker version: `f2c6eb2c-9b54-4b90-9c45-758ce715bcf3`
- Custom domains: `fennec.runta.com` (canonical) and `runfennec.com` (alias),
  managed through `deploy/worker.toml`.
- `npm run preview` serves the built static assets through local Wrangler on 4188.
