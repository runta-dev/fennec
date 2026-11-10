# References

Reviewed on 2026-11-06:

- https://github.com/yetone/magpie — Go entry point, `internal/cli`, `internal/daemon`, `internal/gui/assets`, `native`, `build`, and `site`. Fennec adopts only the currently useful layout, with selected MIT-licensed agent assets documented in THIRD_PARTY_NOTICES.md.
- https://github.com/yetone/magpie/blob/main/internal/gui/assets/app.css — compact utility UI, sidebar/settings surfaces, light/dark theme approach. Fennec uses original HTML/CSS and Runta colors.
- https://runta.com/ — public stylesheet `/assets/index-DkVS_Ob_.css` exposes `--accent:30.291 89.565% 54.902%` and the same ring/glow hue. Fennec's accent is sourced from this value rather than an invented orange.
- https://frontierharness.org/ — existing Runta-backed evaluation entry point; future Fennec handoff should be compatible with reusable evaluation environments.

The macOS shell and bundled UI live in `native/macos/`; development builds are packaged in the ignored `build/` directory.

Runta account logo: copied from the main Runta repository, `dashboard/src/assets/runta-logo-icon.png` (local `runta-forge/worktrees/trail`). Used unchanged in `native/macos/UI/icons/runta-logo-icon.png`.
