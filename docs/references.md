# References

Reviewed on 2026-11-06:

- https://github.com/yetone/magpie — Go entry point, `internal/cli`, `internal/daemon`, `internal/gui/assets`, `native`, `build`, and `site`. Fennec adopts only the currently useful layout, without copying code, assets, provider routing, or its large feature surface.
- https://github.com/yetone/magpie/blob/main/internal/gui/assets/app.css — compact utility UI, sidebar/settings surfaces, light/dark theme approach. Fennec uses original HTML/CSS and Runta colors.
- https://runta.com/ — public stylesheet `/assets/index-DkVS_Ob_.css` exposes `--accent:30.291 89.565% 54.902%` and the same ring/glow hue. Fennec's accent is sourced from this value rather than an invented orange.
- https://frontierharness.org/ — existing Runta-backed evaluation entry point; future Fennec handoff should be compatible with reusable evaluation environments.

Future native packaging can introduce `native/` and `build/` when there are real platform files to maintain. No marketing `site/` is scaffolded yet.
