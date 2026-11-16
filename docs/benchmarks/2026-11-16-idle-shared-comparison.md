# Unloaded comparison on one warm SSH connection

Client: **Mac, Singapore / Singtel**, reported by the user. Destination:
**GCP runta-integration / us-west1**, `ssh.runta.xyz`. Inventory was checked
around 12:04 UTC; previously identified worker `runta-integration-worker-xfs-04`
was in `us-west1-b`. Runtime placement and VPN/proxy routing were not re-resolved
for this run. This is not a US-local measurement.

The user requested no-load measurements. The probe generated **no bulk traffic,
agent input, or scrolling**. Existing live traffic was not stopped or controlled,
so "unloaded" means no generated test load, not an otherwise idle machine/link.

At **12:48:38–12:50:29 UTC on November 16, 2026**, the new
`profile-ssh-latency -idle-terminals` mode opened one authenticated SSH connection,
three persistent resident channels, and one shared small JSON echo channel.
Three warm-up pings per resident were excluded. Six permutations alternated
resident order, and ping/echo/gateway measurement order rotated. All 180 sample
triplets completed successfully, **60 per cloud variant**, without reconnecting.

The Mac sender stayed at **160/128 KiB/s** for all samples. Only the staged cloud
variants differed. This is not the earlier experiment where both endpoint
budgets changed with a fresh connection per block. Cloud binaries are the same
checked artifacts used for the previous 128 KiB experiment; each has its own
diagnostic broker directory. The installed application was not replaced.

| Cloud total/bulk budget | TUI median | TUI P95 | TUI maximum | Guest echo P95 | Gateway P95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 160/128 KiB/s | 189.207 ms | 366.945 ms | 489.304 ms | 466.972 ms | 197.217 ms |
| 128/128 KiB/s | 166.926 ms | 348.362 ms | 507.616 ms | 336.266 ms | 197.641 ms |
| 128/96 KiB/s | 167.168 ms | 322.044 ms | 366.999 ms | 347.095 ms | 195.638 ms |

Tail latency above 300 ms remains without generated bulk load. The ordinary
guest echo also has slow replies, while the gateway endpoint's P95 is about
196–198 ms. This does not locate the cause of the slow requests or establish
that one QoS budget consistently outperforms another. Do not subtract these
independent P95s as component timings or compare them as a controlled before/after
pair with prior loaded runs. No rendered-input latency is measured.

Reproduction uses the fixed Mac sender configuration of the probe build:

```sh
profile-ssh-latency -load=false -samples 60 -timeout 4m \
  -remote /tmp/fennec-qos-tcp-correlated-probe \
  -idle-terminals /tmp/fennec-q128-a160-terminal,/tmp/fennec-q128-b128-terminal,/tmp/fennec-q128-c128-terminal
```

[Raw samples and provenance](2026-11-16-idle-shared-comparison.json) include
source identities, binary/source checksums, location/provider evidence, exact
boundaries, timestamps, budgets and counts. The existing diagnostic race suite,
vet and English/localization checks passed. No production budget changed.
