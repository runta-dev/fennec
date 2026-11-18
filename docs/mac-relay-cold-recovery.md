# Mac relay cold-recovery probe budget

On 2026-11-18, the paired Mac was reachable, but the cloud relay repeatedly
received forced repair requests. The cloud log showed repairs approximately
every twenty seconds starting at 10:53:40 UTC. The guest SSH journal contained
new logins but no SSH service restart during this window. Direct paired SSH and
a diagnostic connection with both data and health handshakes succeeded while
the connector status remained `connecting`.

A live invocation of `ProbeMac` captured this sequence:

1. The existing multiplexed probe failed with exit status 255.
2. The fresh, authenticated probe succeeded.
3. After stopping the old ControlMaster, the final multiplexed probe was killed
   at its context deadline.

That final attempt creates a new ControlMaster and performs the data and health
SSH handshakes. It incorrectly reused the four-second warm-operation budget,
rather than the ten-second cold-handshake budget already used by a fresh probe.
The resulting error made the supervisor force-repair a reachable relay, dropping
connections and restarting the cycle.

Recovery now gives the replacement master the cold-handshake budget. Ordinary
warm checks retain four seconds, the fresh verification remains mandatory,
identity and host-key verification are unchanged, and no user command is replayed.
The supervisor's overall budget includes the replacement's cold attempt.

The regression test injects a failed master followed by successful fresh and
replacement probes; it verifies that only the replacement gets the cold budget
and that the old master is retired only after fresh verification. A live probe
failed before the change and passed afterward. Neither result is a general
network-latency or end-to-end application performance claim.

Diagnostic provenance: UTC 2026-11-18 10:53–11:03; cloud checkout `f8ec95e` plus
this patch; installed cloud terminal `dfd21b132d1596bcf8b0e1dd4c3299294a6b4834`;
client is the user's paired Apple Silicon Mac (physical location and access
network/provider unknown); server is the user's Runta Linux x86_64 computer
(infrastructure provider, region and zone unverified). The live diagnostic made
one failing pre-change and one successful post-change complete `ProbeMac` call.
Its boundary includes the failed old path, fresh verification, retiring the old
master and verifying its replacement. This diagnoses the recovery loop, not all
historical disconnects or the initial trigger for the first stale connection.
