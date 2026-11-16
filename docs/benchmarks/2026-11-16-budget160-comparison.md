# 160 KiB/s application budget experiment

Client: Mac on **Singapore / Singtel**, reported by the user. Destination:
**GCP runta-integration / us-west1**, through `ssh.runta.xyz`; GCP inventory
was checked at approximately 12:04 UTC. The previously identified worker is
`runta-integration-worker-xfs-04`, zone `us-west1-b`; the runtime mapping was
not re-resolved for each block. This is not a US-local measurement.

The user requested 128 + 32 KiB/s to avoid application throttling below the
host limit. Commit `ad8c138` changes total/bulk budgets from **112/96 to
160/128 KiB/s**, retaining priority, fairness, 2 KiB bursts, and the fixed
16 KiB socket credit. The dynamic terminal window derives from the new rate
and retains its 8–64 KiB bounds. TCP backlog feedback remains default-off.
The new `traffic-v3` directory avoids silently reusing an old-rate broker.
Existing brokers are not killed; coexistence is not one aggregate budget.

The experiment ran from **12:13:57 to 12:16:44 UTC**, November 16, in ABBA
order (112, 160, 160, 112). Each block used its own authenticated connection,
with matching Mac and cloud rate settings, 20 idle, 20 loaded, and 20 recovery
samples. Load repeatedly streamed a fixed payload through resident `cat`,
verified every return by SHA-256 and required successful exit. All blocks
completed. Background live traffic was uncontrolled. Baseline used a dedicated
diagnostic broker directory to avoid competing for the production broker's
tokens; the candidate used its new, not-yet-deployed broker directory.

| Aggregate metric | 112/96 KiB/s | 160/128 KiB/s |
| --- | ---: | ---: |
| Loaded TUI P95 (40 samples each) | 259.997 ms | 303.803 ms |
| Loaded TUI median | 203.810 ms | 194.485 ms |
| Loaded TUI maximum | 327.127 ms | 472.705 ms |
| Loaded gateway P95 | 210.292 ms | 222.876 ms |
| Effective verified payload per direction | 43.396 KiB/s | 44.092 KiB/s |
| Idle TUI P95 (40 samples each) | 171.257 ms | 275.670 ms |
| Recovery TUI P95 (40 samples each) | 224.735 ms | 271.945 ms |

The throughput increase is about **1.6%**. Loaded TUI P95 is higher in this
run, but idle and gateway distributions also changed. These observations
establish neither a stable improvement nor the budget's isolated causal cost.
The per-block loaded P95 values were 250, 314, 214, and 260 ms respectively;
neither candidate block should be selected alone as the conclusion. Throughput
is completed application payload including command/drain time, not raw link
capacity. The fixed socket credit and protocol overhead remain possible limits;
raising the pacer does not prove that its previous rate was the bottleneck.

[Raw reports and provenance](2026-11-16-budget160-comparison.json) retain all
240 sample triplets, timestamps, source identities, binary checksums, and exact
boundaries. This tests protocol responsiveness under command traffic, not
actual UI scrolling, key echo, or multi-terminal repaint. It is not the earlier
original-version unloaded baseline. No 240 ms goal achievement is claimed.

Linux race tests passed for traffic, trafficbroker, terminalstream, and the
diagnostic; vet, English/localization checks and UI checks passed. Mac race
tests passed for traffic, trafficbroker, wire-window and socket-window coverage.
The staged package passed complete signature and package/Chromium verification,
using the stable development certificate and unchanged credential helper.

After this experiment, the user-requested budget was installed from clean
`ad8c138` at approximately **12:20 UTC**. Both the Mac app and installed cloud
terminal reported that revision. `fen start` completed; account status was
authorized, broker ready, and connector connected. The installation preserved
the dynamic window and relay recovery fixes. Old session-owned brokers remain
alive where needed; only new-generation clients use the new budget.

The installed warm-operation suite finished at **12:21:30 UTC** with exit 1:
the 500 ms gate is still unmet. For example, `sessions.send` took 951.828 ms,
including 716.110 ms in `herdr_send`; another took 820.692 ms, including
611.980 ms in `herdr_send`. Send and input-ready launch checks failed. These
are operation timings on Singapore/Singtel to integration/us-west1, not TUI
ping RTT or a matched old/new operation comparison. Installation does not turn
the mixed WAN results into a performance improvement. Actual scrolling and
key-echo comparison remains outstanding.
