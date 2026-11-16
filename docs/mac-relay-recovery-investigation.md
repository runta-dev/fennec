# Intermittent Mac relay failures

## Observed on November 16, 2026

The Mac alias intermittently returned `ssh: unexpected packet in response to
channel open: <nil>`, followed by successful commands without a user restart.
Checks used `ControlMaster=no` and `ControlPath=none`, so this occurrence cannot
be attributed solely to an old OpenSSH ControlMaster. At 11:12:16 UTC the Mac
reported seven days of uptime, with the same Fennec daemon PID 51062 running
since 09:58:40 UTC. This does not prove continuous network reachability, but
does rule out a Mac or daemon restart during that failure window.

The guest system journal records `ssh.service` stopping at **11:07:41 UTC** and
starting again at 11:07:42. At 11:07:41 the guest agent reported that configuring
platform SSH failed because `/run/sshd` was missing. A new SSH login followed
at 11:07:43. Another SSH service restart occurred at 09:49:07–09:49:10. These
are observed upstream disturbances, not evidence that the Mac was offline.
The journal alone does not identify the caller that requested configuration,
nor establish the cause of every historical reconnect.

Read-only inspection of the active Forge slot (`spark`) found an ordering defect
in `crates/runta-guest-agent-server/src/platform_ssh.rs`: the configure script
creates `/run/sshd` before stopping `ssh.service`, then runs `sshd -t`. The
guest's service has `RuntimeDirectory=sshd` and `RuntimeDirectoryPreserve=no`,
so stopping it removes that directory. This explains the logged validation
failure; directory creation must occur after the stop as well. No Runta source
or deployed guest-agent change is included in this Fennec patch.

Another handshake EOF occurred around a concurrent experimental installation.
The installed app and cloud terminal changed to `18b3658` (32 KiB terminal
output window), and the Mac daemon restarted at 11:21:10 UTC. That experiment
has since been declared finished by the user. Its terminal-window change is
retained in the merged recovery build, rather than reverted during installation.

The cloud byte-relay process was PID 246255, started November 13. Its recorded
revision matched both its installed source and the repository. An old process
start time alone therefore did not establish that it was stale. Its log was
empty. The Mac daemon discarded the reverse link's returned error, and its
status file continued to say `connected`; historical failure attribution was
not possible from those files.

## Reproduced recovery defects and changes

A regression reproduces an independent failure amplification: closing one idle
resident bridge canceled the whole reverse relay, closing an active connection
with it. The supervisor then retried with exponential backoff up to 30 seconds,
without resetting that delay after a successful connection.

The resident relay now replaces only the failed parked bridge. Each slot retries
with a bounded 100 ms–2 s backoff through the existing shared resident recovery
path. Active connections retain their own lifetimes, while account cancellation
and an offline transition still close all owned streams. Failures opening a
bridge, waiting for its claim marker, and reaching the local SSH service are
logged with their phase. Link termination updates the status to `reconnecting`
and records the error; a successful link resets supervisor backoff.

The cloud SSH relay also had a race between channel opening and its background
connection watcher. A failed channel open could return the cached dead client
before the watcher cleared it. The relay now invalidates that generation
immediately and retries channel opening once with the same identity and host-key
checks. Explicit channel rejection does not discard a healthy connection.
Commands are never replayed after exec/shell/subsystem forwarding begins.
Pool refills are fenced by connection generation. New aliases use a version-3
ControlPath so new commands do not attach to an older relay implementation.

The stderr message now describes failure to open the Mac relay connection;
it no longer treats every channel-open error as proof that the Mac is offline.

## Validation and limits

The idle-bridge regression failed against the previous implementation, then
passed with the fix. Fault tests cover an active connection surviving both an
idle bridge EOF and its replacement's first failed open, parent cancellation,
a cached dead SSH connection before watcher cleanup, delayed old-generation
cleanup, and explicit channel rejection. The three new recovery cases passed
ten repetitions with the race detector. Existing authentication, one-round-trip
command, output/exit-status and cancellation tests remain required.

This repairs recovery behavior; it does not prevent the upstream platform from
restarting SSH, preserve an executing command across a real transport loss, or
prove why every historical disconnect occurred. Actual Mac build/install and
live connection validation must be recorded before claiming deployment.
