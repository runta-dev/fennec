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

The Fennec source repository is public. Get started links to the repository
setup instructions, and the header/footer expose GitHub. No published release
was available when configured; the page does not advertise a nonexistent DMG.
When a release is available, update the CTA and preview notes together.

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
- Custom domain: `fennec.runta.com`, managed through `deploy/worker.toml`.
- `npm run preview` serves the built static assets through local Wrangler on 4188.
