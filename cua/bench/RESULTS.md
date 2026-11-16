# macOSWorld results

The three evaluation sets (`bench/sets`) × 3 trials per backend, run on 2026-11-15 and 2026-11-16 with `cmd/cua-bench` from main `f410333`. Every trial runs in a fresh tart clone of one golden macOS 26 image, the same for every backend and every set. The same host-side agent loop drives every backend: model `zai/glm-5.3-flashx` through Magpie, a 40-turn cap and a 10-minute wall clock. Only macOSWorld's grading commands decide a score.

## Held-out set (60 tasks, sealed)

The held-out set was never used for tuning; only its aggregate scores are reported.

| Backend | Solved / scored | Rate | Trial 1 | Trial 2 | Trial 3 | Mean turns | Mean tokens | Mean agent time |
|---|---|---|---|---|---|---|---|---|
| **fennec-cua** | **123 / 179** | **68.7%** | 42 | 41 | 40 | 19.2 | 277k | 2m17s |
| cua-driver | 116 / 179 | 64.8% | 39 | 38 | 39 | 20.9 | 862k | 2m4s |
| shuttle | 105 / 178 | 59.0% | 38 | 36 | 31 | 23.3 | 202k | 1m33s |

Paired by task (each task's solve rate, fennec-cua minus the other backend, 10,000 bootstrap resamples over the 60 tasks):

| Comparison | Difference | 95% CI | Tasks better / worse / tied |
|---|---|---|---|
| fennec-cua − shuttle | +10.0 points | +0.7 to +19.2 | 18 / 6 / 36 |
| fennec-cua − cua-driver | +4.6 points | −2.7 to +12.2 | 10 / 7 / 43 |

fennec-cua solves significantly more held-out tasks than shuttle. Against cua-driver the difference is within noise; fennec-cua uses about a third of its tokens.

## Development and safety sets (used for tuning)

These transcripts were read to find and fix fennec-cua's failures, so their scores favour fennec-cua; the held-out set is the fair comparison.

| Backend | dev (30 tasks) | dev tokens | safety (19 tasks) | safety tokens | gold | distracted |
|---|---|---|---|---|---|---|
| **fennec-cua** | **82 / 90 (91.1%)** | 99k | **44 / 57 (77.2%)** | 238k | 23 | **0** |
| cua-driver | 73 / 90 (81.1%) | 457k | 41 / 57 (71.9%) | 806k | 3 | 8 |
| shuttle | 72 / 90 (80.0%) | 171k | 35 / 57 (61.4%) | 238k | 1 | 6 |

Safety tasks show a distracting dialog at the first turn: gold means the agent dismissed it, distracted means it pressed the dialog's action; the remaining runs left it open.

## Notes

- Setup Assistant's Update Mac Automatically pane opened over the desktop in about a third of the trials on earlier images, for every backend. The image now blocks Apple's update servers and quits Setup Assistant during trials; it appeared in none of the 441 dev and safety trials (held-out runs keep no transcripts to check). Results from earlier images (2026-11-13 to 2026-11-15) are not comparable with these and are not reported.
- Two dev tasks score 0 for every backend because their upstream graders are broken (`dbfb0512`: `grep -q "-7.000000"` is parsed as an option; `17fed363`: a stray backtick in its `pre_command`). Grading reproduces macOSWorld unchanged; `cua-bench run -corrected` runs them with a documented correction, reported apart.
- Harness failures (model gateway errors) were retried and are not scored; a held-out trial that failed all three attempts is left unscored, which is why some backends have 178 or 179 scored runs.
- Known differences: cua-driver has no model-backed features and sends 62 tool schemas (about 140 KB) every turn; shuttle runs with its defaults, including `script_run`.
- The held-out set has now been scored once. Draw a new one (`cua-bench select` with another seed) before using held-out scores to judge further tuning.
- Per-task results stay with the run outputs. `cua-bench web` serves the results page from them, with the held-out set as rankings only. Task texts and grading commands never leave the local macOSWorld checkout (CC BY-NC 4.0).
