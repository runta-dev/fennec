# Queues: replicated requests and cloud-primary scheduling

This is the implementation contract for Fennec Queues. Acceptance evidence is
recorded separately; this design alone does not establish installed behavior.

## Product boundary

Fennec is a Mac menu-bar application, its resident Go daemon, and one persistent
cloud computer per user. Desktop and CLI are clients of that daemon. Queue
coordination stays inside these two endpoints over the existing authenticated
resident channel. There is no additional public service or third coordinator.

Mac replicas are isolated by API environment, verified owner, and cloud computer
ID under `~/.fen/boards/<scope>/`. Each computer has its own projects, journal,
sync cursor, attachments and launch locks. Switching computers selects that
replica and restarts its scheduler; the resident sync channel is pinned to the
same scope. A newly provisioned computer starts empty, then receives its starter
queue. Switching back restores the original replica. Legacy `~/.fen/board` data
is preserved without automatically merging it into any computer.

Desktop queue selection, text drafts and attachment drafts are scoped in
`desktop-board-state.json`. Queue actions capture their scope and reject stale
responses after a switch. Local agents use `fen queue --scope <scope>` so a late
completion returns to its original board. Archive import remains an explicit
operation on the selected cloud computer and never rebinds another local replica.

Projects are presented as **Queues**. **Request** and **New Request** remain
user-facing terms. Execution settings, shared instructions, optional repository,
knowledge and default agents belong to the queue. Agent **Run location** remains
the existing agent preference; do not introduce another queue location switch.

## Request, run and session

`Queue -> Request -> Run -> herdr-core session` is the identity chain. Requests
retain their discussions and results across attempts. Every run has a durable,
unique ID, an execution location and a herdr-core session identity. Persist the
run before dispatch, label the session with its queue/request/run, and expose it
in the existing Sessions view. Requests open their sessions; sessions return to
their requests. Reattachment is not a new run. Retry creates a new run; it never
silently retries an ambiguous launch. A continuation is an explicit new run,
with the preceding discussion as context.

The scheduler selects work and allocates execution slots. herdr-core owns actual
processes, PTYs, attachment and termination. Do not add another process manager
or spawn agents outside herdr-core. The execution location is fixed when a run
is assigned. Changing an agent preference affects future assignments only.

## Primary and backup

The cloud scheduler is a persistent cloud-local Fennec service independent of
the Mac connection and terminal attachment. Normally it schedules both local
and cloud runs. The Mac daemon maintains a complete replica and a standby
scheduler. Closing the UI does not stop either scheduler or existing sessions.

The backup is scoped: when the cloud is unreachable it can schedule only local
work for which the Mac owns an execution slot. It cannot infer that a cloud run
stopped or take cloud execution ownership based on a timeout. The cloud may
still be running on the other side of a network partition.

Local execution accepts a persisted scheduler generation. Entering standby
execution fences old cloud dispatches on the Mac. Reconnecting first merges
records and reconciles executions, then acknowledges a new generation before
the cloud resumes dispatching local work. No timeout grants global leadership.

## Capacity

The configured queue limit is global. Its durable slot allocations have local
or cloud owners; their total never exceeds that limit. Normal cloud scheduling
can transfer idle slots only through an acknowledged handoff. A disconnected
Mac uses only its already-owned slots. The cloud cannot reuse a local slot
because its Mac went offline. An uncertain run continues occupying its slot.
When no safe local slot is available, requests remain queued.

## Replication

Both endpoints keep requests, comments, queue settings, order, execution
records and results on durable local storage. Mutations have stable operation
IDs and source sequence numbers. Persist before acknowledging; incremental
exchange uses durable cursors and deduplicates retransmission. A lost sync reply
must be safe to replay. Isolate replicas by the paired account/runtime/device.

Do not overwrite one complete board with another. Comments append and merge by
ID; queue settings and ordering carry versions; run observations belong to the
execution endpoint. Pending user intent is distinct from confirmed execution.
An offline pause is saved immediately but cannot claim to have stopped remote
dispatch until acknowledged. Late results address their exact run, not merely
the request's latest session. Keep concurrent edits and completed-run history.

## Disconnect and recovery

| Situation | Required behavior |
| --- | --- |
| Mac offline | Cloud requests continue; local work and Mac-dependent actions wait |
| Cloud unreachable | Local operations persist; scoped backup can execute eligible local requests |
| Cloud run unreachable | Keep its last observation and timestamp; never mark it failed just for loss of contact |
| Launch receipt lost | Query herdr-core by run identity; do not allocate a replacement run |
| Process exits without a result | Record a failed run after an authoritative observation; offer explicit retry |
| Connection restored | Exchange missing records, reconcile runs and slots, apply pending intent, acknowledge dispatch handoff |

Recovery precedes new dispatch. Match run IDs to authoritative herdr-core
sessions: live sessions reattach, completed results merge, confirmed exits fail,
and absent/ambiguous receipts remain unknown. Existing runs retain their
execution location. UI content remains available while its freshness is unknown.

## Interaction

The queue shows Needs you, Running and Queued groups, plus Failed and Done.
Waiting requests support drag ordering and Run next. Pause/resume and concurrency
controls live only in Queue settings > Execution, not on the request-list page.
Queue pause prevents new
assignments, not current execution; stopping a run remains a separate session
operation. Comment appends without scheduling. Continue explicitly enqueues
another run. Needs you releases a slot but preserves the old session and result.
The former Queue settings tab becomes Execution. Knowledge is shared queue
context. Creation asks for purpose, default agent and optional repository.

## Request images

New Request accepts clipboard images and copied PNG/JPEG files. The composer
shows removable thumbnails, permits an image-only request, and opens the
original image when a thumbnail is clicked. Text paste remains unchanged.
Native TIFF/BMP clipboard representations are converted to PNG in the UI, as
in the existing terminal clipboard adapter. Each request allows eight images,
up to 8 MiB each, matching herdr-core's `terminal.put` limit.

Go validates images and keeps content-addressed originals in private board
storage. The operation log carries references and small thumbnails; originals
transfer in resumable, checksummed chunks over the existing resident channel.
Requests cannot be claimed while an original is missing on their execution
endpoint. Draft images are not replicated until attached to a posted request.

Image-bearing runs start their normal herdr-core TUI without a CLI task prompt.
They use `terminal.put` (the same operation behind `hc put`) to stage the images,
then `terminal.send` for mode-aware paste and submission of their paths and the
request. There are no new agent-specific image flags and no background writes
to the user's clipboard. A durable input marker prevents resubmission after a
lost reply; a live process alone cannot confirm that ambiguous image input.
Agent-facing request reads omit thumbnail base64 from text context.

## Acceptance gates

Use isolated test homes and fake executors for fault tests, then real disposable
herdr-core sessions for integration. Verify both Run locations, Sessions
visibility and links, concurrent claim rejection, pause/order enforcement,
comments versus continuation, result fencing, durable restart, replayed sync,
divergent offline edits, partitioned scheduling, lost launch receipts, capacity
conservation and recovery handoff. Never submit model prompts or stop user
sessions merely to test scheduling. Build/sign/install full matching artifacts
and validate real UI behavior before claiming installed acceptance.
