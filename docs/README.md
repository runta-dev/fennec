# Documentation index

Reviewed on 2026-11-17 against source `9553de0`. Source capabilities, installed
behavior, and release acceptance are separate evidence levels. This cleanup did
not rebuild the application or rerun historical live benchmarks.

## Current entry points

- [User guide](../README.md) and [Simplified Chinese guide](README_ZH.md): supported
  agents, setup, sessions, local access, and billing boundaries.
- [Design](../DESIGN.md): implemented architecture and remaining offline-task
  requirements. The old scaffold descriptions have been removed.
- [Development](../DEVELOP.md): builds, signing, source checks, and installed tests.
- [Release requirements](../TODO.md): acceptance gates; unchecked means not accepted,
  not necessarily unimplemented.
- [Backlog](../BACKLOG.md): remaining billing and account items.

## Implementation references

- [Native credentials](native-agent-credentials.md), [managed Magpie](magpie-runtime.md),
  and [gateway lifetime](cloud-gateway.md).
- [Transport lifecycle](transport-worker.md), [TUI reattachment](tasks/tui-auto-reconnect.md),
  [predictive echo](predictive-echo.md), and [cmux hooks](cmux-hooks.md).
- [Cloud handoff workflow](../skills/cloud-handoff/SKILL.md): use available tools;
  synchronization is not a production offline-readiness guarantee.
- [Hidden dependency audit](hidden-dependency-audit.md): source inventory, not
  clean-device acceptance.
- [Codex Desktop research](codex-desktop-cloud.md): protocol research with its own
  acceptance boundary; CLI support does not imply Desktop support.

## Historical evidence and investigations

- [Archived acceptance records](archive/README.md): previous candidates, onboarding
  observations, and the superseded acceptance snapshot. Preserve failures as well
  as passes; neither proves the current installed state.
- [Performance ledger](performance-baseline.md): measurements tied to named builds.
- [Terminal responsiveness](tui-responsiveness-proposal.md): current source summary
  followed by dated implementation and experiment records.
- [SSH backlog investigation](ssh-backlog-investigation.md) and
  [Mac relay investigation](mac-relay-recovery-investigation.md): historical diagnoses.
- [Benchmark provenance](benchmarks/README.md): raw artifact catalog, measurement
  boundaries, source versions, and known metadata gaps.

When updating a feature, update its current reference and retain dated evidence
separately. Do not append contradictory present-tense implementation claims to a
design document. A new source commit does not retroactively turn a historical
failure into a pass or an old proposed experiment into a current instruction.
