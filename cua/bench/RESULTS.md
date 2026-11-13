# macOSWorld results

The 30 macOSWorld tasks of the dev set (`bench/sets/dev.txt`) × 3 trials per backend, run on 2026-11-13 with `cmd/cua-bench`. Every trial runs in a fresh tart clone of the same golden macOS image. The same host-side agent loop drives every backend: model `zai/glm-5.3-flashx` through Magpie, a 40-turn cap and a 10-minute wall clock. Only macOSWorld's grading commands decide a score.

| Backend | Solved / 90 | Trial 1 | Trial 2 | Trial 3 | Mean turns | Mean tokens | Mean agent time |
|---|---|---|---|---|---|---|---|
| cua-driver (raw MCP tools) | 74 (82.2%) | 24 | 25 | 25 | 11.3 | 448k | 1m5s |
| shuttle 0.1.50 | 67 (74.4%) | 24 | 21 | 22 | 21.1 | 164k | 1m33s |
| fennec-cua, six-tool surface (d750daa) | 74 (82.2%) | 25 | 26 | 23 | 18.1 | 563k | 1m24s |
| **fennec-cua, Codex-compatible js REPL** | **77 (85.6%)** | 27 | 26 | 24 | 18.9 | **137k** | 1m22s |

## Notes

- The REPL surface is fennec-cua's only surface now. Compared with the six-tool surface it solved three more trials and used about 4× fewer tokens; compared with raw cua-driver, about 3× fewer.
- Two tasks score 0 for every backend because their upstream graders are broken, so the reachable maximum is 84, not 90:
  - `dbfb0512`: the grader runs `grep -q "-7.000000"`, which grep parses as an option.
  - `17fed363`: its `pre_command` starts with a stray backtick and always fails.
  Grading reproduces macOSWorld unchanged, so both stay at 0. `cua-bench run -corrected` now runs them with a documented correction, reported apart (`bench/sets/README.md`).
- Harness failures (transient gateway DNS errors) were retried and are not scored: one each for cua-driver and the six-tool fennec-cua, none for the others.
- The comparison has these known differences:
  - Shuttle and fennec-cua reach the same model through the same relay.
  - cua-driver has no model-backed features, and it sends 62 tool schemas (about 140 KB) on every turn.
  - Shuttle runs with its defaults, including `script_run`.
  - The REPL run used a golden image rebuilt with the new fennec-cua binary. The other three runs share the earlier image.
  - Some cua-driver and shuttle trials ran at concurrency 1, the rest at concurrency 2.
- Per-task results stay with the run outputs (`results*/summary.md`, written by `cua-bench run` and `cua-bench summary`). `cua-bench web` serves the results page from them. Task texts and grading commands never leave the local macOSWorld checkout (CC BY-NC 4.0).
