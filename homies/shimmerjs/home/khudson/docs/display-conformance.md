# Display lifecycle conformance

The routine check runs without changing display configuration, posting input,
or requiring an Aqua session:

```sh
nix build .#checks.aarch64-darwin.khudson-display-conformance-aw-chainguard
nix build .#checks.aarch64-darwin.magicbus-conformance-aw-chainguard
nix build .#checks.aarch64-darwin.khudson-posture-aw-chainguard
```

`result/conformance.jsonl` contains Go's test events, scenario names, failures,
and skips. The Go checks vet and race-test every khudson and magicbus package and
enforce Go formatting on owned source, excluding vendored dependencies. The
launcher process tests use temporary sockets and disposable shell children;
the virtual-time scenarios use no child processes or display APIs.

## Apple contract and evidence limits

| Boundary | Documented behavior | Harness coverage |
| --- | --- | --- |
| Register | Registration returns `CGError`; the same callback may have multiple registrations with distinct user data. | Each documented error code plus an unknown code; failed registration cannot become ready. |
| Dispatch | Callbacks execute on the event-processing thread while the app processes events, or within the reconfiguration call made by that app. | Delivery is injected. Actual event-loop routing is not established by this check. |
| Begin | Begin callbacks occur per online display before reconfiguration. | Count receipt; do not request a snapshot from the begin phase. |
| Complete | Completed callbacks occur per added, removed, and online display; CoreGraphics state is current. Queries using a removed display ID fail. | Coalesce bursts; preserve a pending wakeup; removal requests enumeration rather than querying the removed ID. |
| Snapshot | The native enumeration result and the returned display list are separate values. | Failed scans retain the last known state and raw error code. Valid empty lists signal removal; malformed query responses remain errors. |
| Reconcile | Apple does not document acknowledgment or redelivery based on whether application work succeeds. | A failed query or unresolved layout retries without a second notification; a successful reconciliation disarms the retry. This retry rule is khudson policy. |
| Remove | Removal requires the callback and user-data pair used at registration and returns `CGError`. | Preserve the original token across a failed removal; fence late delivery; retry removal; prevent old tokens from reaching new subscriptions. |
| Cancel | The owner must stop its worker and release owned resources. | Cancellation during retry, source closure, and startup failure; no work after shutdown. This ownership rule is khudson policy. |

Sources: Apple's [configuration notification guide](https://developer.apple.com/library/archive/documentation/GraphicsImaging/Conceptual/QuartzDisplayServicesConceptual/Articles/Notification.html),
[callback contract](https://developer.apple.com/documentation/coregraphics/cgdisplayreconfigurationcallback),
[registration](https://developer.apple.com/documentation/coregraphics/cgdisplayregisterreconfigurationcallback(_:_:)),
[removal](https://developer.apple.com/documentation/coregraphics/cgdisplayremovereconfigurationcallback(_:_:)),
[CGError definitions](https://developer.apple.com/documentation/coregraphics/cgerror),
and [run-loop management](https://developer.apple.com/library/archive/documentation/Cocoa/Conceptual/Multithreading/RunLoopManagement/RunLoopManagement.html).

The local SDK's `CGDisplayConfiguration.h` also explicitly says callbacks run
on the event-processing thread. It does not say that any private thread which
registers the callback becomes that thread. A Core Foundation loop must have
an input source in the mode being serviced. Keeping an otherwise empty loop
alive does not establish that it services WindowServer notifications.

In historical commit `03264f3`, `internal/cgdisplay/watch.c:27` ignored the
registration result and `watch.go:73` created a private thread for dispatch.
That code cannot establish successful registration. Its failure to receive
events does not establish an unreliable Apple callback mechanism. Neither
observation establishes the cause of the October 7 input freeze.

## Implementation boundaries

The native shim is [notifications.c](../khudson/internal/cgdisplay/notifications.c).
Its registration/removal functions are replaced in
[notifications_test.go](../khudson/internal/cgdisplay/notifications_test.go).
The tests run the same token ownership, phase filtering, coalescing and
teardown implementation exposed by `cgdisplay.Subscribe`.

[conformance_test.go](../khudson/internal/hudlaunch/conformance_test.go) exercises
the production `monitorChild` function with injected query results and child
completion. Go's `testing/synctest` supplies virtual time. The suite covers a
full virtual day after successful reconciliation to catch accidentally armed
timers, and a notification burst during a failed query's retry interval.

The CLI now runs `RunApplication` in
[application.go](../khudson/internal/hudlaunch/application.go). The initial OS
thread is locked in `main.init`; other commands unlock it. The native adapter
creates a prohibited-activation `NSApplication`, registers before the initial
snapshot, and owns `NSApplication.run` on that thread. Worker shutdown queues
`stop` and an application-local wake event. Apple documents that `stop` alone
does not forcibly wake the loop. Removal runs on the owning thread after the
worker and loop stop, with three bounded attempts.

Sources: [NSApplication.run](https://developer.apple.com/documentation/appkit/nsapplication/run()),
[NSApplication.stop](https://developer.apple.com/documentation/appkit/nsapplication/stop(_:)),
and the local SDK's `CarbonEventsCore.h` declaration of
`IsSecureEventInputEnabled` (main-thread sampling; the API is not thread safe).

Completed notifications coalesce for 250 ms. Successful registration uses a
one-minute reconciliation scan; failed registration falls back to two seconds
and records the native error. The scan uses documented display-ID enumeration
and bounds APIs, without UUID resolution or mode copying. A disappearing ID
with unavailable bounds is an error, not an empty topology. A valid empty
snapshot is absence. Expensive NSScreen queries run initially, on invalidation,
and while retrying failure; a successfully absent target has no query timer.

`callback.delivery` records receipt separately from `watch.reconciliation`.
A minute health record carries begin/completion/late-callback counts, an
AppKit heartbeat, and cached secure-input status sampled on the main thread.
This makes a non-delivering callback distinguishable from failed application
work or a stuck event loop during normal use. A fallback disagreement is
recorded evidence, not proof of a particular Apple defect.

The native code compiles in the checks. Native initialization, dispatch, secure
input probing, and actual hotplug delivery are deliberately not invoked by the
routine harness. Passing fake-boundary tests does not certify macOS behavior.
Operational confirmation comes from naturally occurring changes after deployment;
no display reconfiguration or input injection is required in a development loop.

## Resource ownership and idle behavior

Protocol v2 advertises supported layouts, scraped-frame rendering, and physical
Edge eligibility. The current dock advertises home/keyboard without scraped
frames; unsupported layouts create no renderer demand. Only the launcher's
explicit `dock -edge` client can claim touch geometry or enable sleep assertions.
Legacy clients remain native-state consumers without physical ownership.
See [capabilities.go](../khudson/internal/bus/capabilities.go) and its tests.

In the protocol, a "dock" is a khudson panel UI client connected to the bus.
It is not a hardware docking station or a display. The bus removes a client
when its socket closes/errors or sends no message for 15 seconds. Resource
demand below follows these client connections, not a direct monitor count.

[renderer.go](../khudson/internal/bus/renderer.go) makes the bus own the hidden
renderer. It starts only when a capable connected client's current layout contains an
exec widget. Retirement quiesces scheduler work, stops the process group, and
discards exec bindings. Every replacement gets a distinct socket and fresh exec
widget state. In-flight input uses its captured old client; queued pointer and
config actions carry a local epoch and are dropped after renderer or config
replacement. Old snapshots carry their original widget state and cannot populate
a new generation. Native widget caches
survive a renderer-only transition.

[scheduler.go](../khudson/internal/bus/scheduler.go) includes scrapes in its
single-flight accounting, defers config replacement during adoption, and gates
verification, resize, release, and scrape paths on backoff. Failed adoption no
longer retries every 250 ms or runs without a dock. Native poll deadlines are
capped at 30 seconds. Shutdown also cancels and joins bus workers on listener
failure, rather than only on external cancellation.

[caffeinate.go](../khudson/internal/bus/caffeinate.go) preserves the user's on/off
preference while releasing assertions when the last eligible Edge client disconnects. `-w <bus-pid>`
also releases them on bus exit, including a crash that bypasses cleanup. This
flag's process-exit dispatch source is visible in
[Apple's caffeinate implementation](https://github.com/apple-oss-distributions/PowerManagement/blob/main/caffeinate/caffeinate.c).

Orphan identity/termination failures retain the owner record and prevent a new
child. Renderer recovery confirms process-group exit after termination; denied
probes and groups surviving their unverifiable leader remain errors. A blocked
renderer does not take down native bus functionality. Recovery tests inject
process probes/signals and time passage, without signaling live processes.

The Nix activation retires the old always-on substrate agent and passes the
renderer executable to the bus. It no longer waits for an idle renderer socket.
Tradeoff: exec widgets restart after the last panel client disconnects, or after switching
to a layout with no exec widgets, even if their per-widget keepAlive is true.
Their in-process module state and native caches are separate from that renderer.

## Unattended input observations

[inputdiagnostics.go](../touchd/inputdiagnostics.go) samples separate hardware
and session counters/idle ages for keys, modifiers, clicks, motion, drag, and
scroll every five seconds. It runs even with no Edge or Moonlander attached.
It records timestamps, recorder PID, actual sample spacing, and cumulative HID
report counts. It does not install an event tap, post input, or record key
values, text, or pointer positions.

The Nix launcher selects `log/input-counters.jsonl` under the khudson state
root. Rotation retains the current file plus `.1`, `.2`, `.3`, each at most
4 MiB, with mode 0600 on creation. Write/rotation failures retain typed evidence
and retry once per minute; the first successful write records recovery.
A restart starts a new PID/cumulative-report series; Quartz counters can wrap or
reset with WindowServer, so decreases must not be interpreted as negative input.

Apple describes the [source-state tables](https://developer.apple.com/documentation/coregraphics/cgeventsourcestateid)
and [event counters](https://developer.apple.com/documentation/coregraphics/cgeventsource/counterforeventtype(_:eventtype:)).
They do not certify delivery to an application. Key autorepeat is not counted;
key-up/key-down counts need not balance. Secure input affects keyboard intercept
processes, which is narrower than proving a system-wide keyboard failure:
[Apple TN2150](https://developer.apple.com/library/archive/technotes/tn2150/_index.html).

The old aggregate-idle detector now reports a *suspected* wedge and an end to
idle disagreement, not proven recovery. Its HID totals remain monotonic across
log drains; source-map access is synchronized during arrival. Console inspection
and process-name probes have two-second limits; a failed console read is unknown,
not evidence that secure input is disabled.

## Recorded errors and recovery

`display-lifecycle` records are JSON embedded in the existing launcher log.
They contain `boundary`, `outcome`, `kind`, optional numeric `code`, `action`,
`attempt`, a bounded error message, and bounded subprocess stderr when available.
The action names the behavior selected
by the running code. No input contents or pointer positions are recorded.

| Observation | Current action |
| --- | --- |
| Display absent after successful query | Wait for a topology change; no `osascript` error-retry timer. |
| CoreGraphics scan fails | Record the raw code; retain the last known state and retry the scan. Log the first error, changed errors, exponentially spaced repeats, and recovery. |
| Changed-state query fails or layout is unresolved | Keep the change pending; retry at `RetryDelay`; retain the child until state can be resolved. |
| More notifications while retry is pending | Coalesce into the pending work; do not accelerate or postpone the retry. |
| Query succeeds after failure | Record recovery and clear the retry; stop/reposition the child if required. |
| Application initialization or launcher watch startup fails | Record failure, release local resources, return to the process supervisor. |
| Native notification channel closes | Record failure; use the two-second scan fallback. A closed launcher watch stops its child and returns to the supervisor. |
| Child start/exit or socket claim fails | Record failure; use existing relaunch backoff. |
| Callback removal fails | Fence delivery; retry up to three times; retain the token until successful removal or process exit. |
| HID enumeration fails, including after partial results | Preserve waiters/device state; retry with a separate 2-30 second backoff. Do not deliver partial results or reclassify the device as absent. |
| HID open/read/feature or HID++ request fails | Record boundary, type, raw native/protocol code when available, message, occurrence count, and existing caller recovery policy. |
| Renderer spawn/adopt fails | Back off; keep native modules usable. Quiesce and replace a crashed renderer under a new endpoint. |
| Input queue full or queued pointer/config action belongs to a retired generation | Record and drop the event. Never replay stale input into a replacement renderer. |
| Pointer/key injection fails | Record the widget boundary and error; drop the failed event rather than replaying potentially partial input. |
| State/pidfile write fails | Log the exact boundary; stop a newly launched child if its owner file cannot be persisted. Backoff persistence failure retains the in-memory schedule. |
| Recorder cannot append/rotate | Record permission/storage/other error; retry in one minute without stopping the HID modules. |

Timeout, cancellation, malformed JSON, JSON shape mismatch, missing executable,
filesystem permission, process exit, unresolved layout, closed source, and
each known `CGError` have distinct kinds. Unknown native codes remain numeric
and unknown; they are not relabeled as permission denial or display absence.

There is no established mapping from these observations to the reported
system-wide input freeze. In particular, an error is not sufficient evidence
to restart WindowServer, reset TCC, or ask the user to disconnect a monitor.
A missing executable, unreadable owner file, persistent permission denial, or full
disk can require repairing the installation/path/storage. The logs identify those
boundaries; automatic retries do not repair them. Opaque IOKit text remains opaque
except for preserving an explicitly reported hexadecimal code. It is not relabeled
as a seize, TCC denial, sleeping device, or WindowServer failure without evidence.

## Terminal transport and passive-provider conformance

The Nix display-conformance check also race-tests `internal/socketio`,
`internal/viewtransport`, `surface`, and `internal/module/logiretch`. These tests use in-memory
connections, injected dials, and virtual time for reconnect backoff. They do not
connect a monitor, send input, or launch kitty.

| Scenario | Required result | Evidence |
| --- | --- | --- |
| Bus client stops reading | UI Update and another client's delivery stay responsive; the stalled write expires. | [UI test](../khudson/internal/viewtransport/connection_test.go), [bus test](../khudson/internal/bus/transport_test.go), [writer tests](../khudson/internal/socketio/writer_test.go). |
| Output queue fills | Close the connection, retain the backpressure error, reject further sends. No automatic action replay. | [writer.go](../khudson/internal/socketio/writer.go). |
| Caller mutates queued data | Peer receives the serialized value from enqueue time in order. | [writer tests](../khudson/internal/socketio/writer_test.go). |
| UI exits during dial, before adopting a client, or with a full reader channel | Cancel and join the dial/reader/writer; reject commands started after exit. | [connection tests](../khudson/internal/viewtransport/connection_test.go). |
| Passive provider loses demand while its read or dial is blocked | Cancel, close, join, clear live input state; renewed demand can restart it. | [provider lifecycle tests](../khudson/internal/module/logiretch/lifecycle_test.go). |
| Poll deadline ends | Keep the demanded shared feed alive. Final module Close prevents restart. | [provider lifecycle tests](../khudson/internal/module/logiretch/lifecycle_test.go). |
| Feed accepts and immediately disconnects | Wait before reconnecting; cancellation interrupts that wait. | [virtual-time test](../khudson/internal/module/logiretch/lifecycle_test.go). |
| Multiple widgets share a provider; clients leave | Keep demand while any current widget/client needs it; withdraw on empty layout, pending config, or final client loss. | [demand test](../khudson/internal/bus/demand_test.go). |

The new observation boundaries are `view.bus.dial`, `view.bus.read`,
`view.bus.write`, `bus.dock.read`, `bus.dock.write`, `module.logiretch.dial`,
and `module.logiretch.read`. They use the existing bounded error/recovery
recorder. Backpressure retains its concrete error type; transport timeouts,
EOF, JSON decode errors, and OS error codes retain their classifications.
[Client adapter](../khudson/internal/viewtransport/connection.go),
[server adapter](../khudson/internal/bus/bus.go),
[provider adapter](../khudson/internal/module/logiretch/logiretch.go).

Each transport frame is capped at 8 MiB. The output queue also caps queued and
in-flight payloads at 16 MiB, alongside the 128-message limit.
Overflow closes the connection and forces state resynchronization. Semantic
view actions now enter a separate bounded delivery worker that commits to the
shared journal before sending. Queue submission itself is not durable acceptance;
exit before that commit can lose unaccepted work. No action is automatically
replayed after a transport failure. Protocol v2 leases and visibility generations
are checked again at execution. Hidden integrated and standalone views withdraw
presentation demand. None of these tests proves the OS input freeze's cause.
[Delivery worker](../khudson/internal/viewtransport/connection.go),
[view sessions](../khudson/internal/bus/views.go).

## Prior art

- [Go testing/synctest](https://go.dev/blog/testing-time): virtual time and
  quiescence for concurrent production code. Used here; it does not virtualize
  C calls or provide exhaustive scheduling exploration.
- [Microsoft Coyote](https://microsoft.github.io/coyote/overview/how/): controlled
  concurrent schedules and replayable failures. Useful design precedent; its
  runtime targets .NET rather than this Go/cgo implementation.
- [Apple Testing Tips & Tricks](https://developer.apple.com/videos/play/wwdc2018/417/):
  replace external dependencies with controllable substitutes at test boundaries.

Keep the adapter small. Add new scenarios from documented requirements or
observed errors, and retain the original boundary/code in regression fixtures.

## October 7 investigation status

The reported episode was approximately 14:00 local time on 2026-10-07. Pointer
motion continued while keyboard/click input failed; unplugging an ordinary
external display ended the symptoms. This is user-reported timing, not a measured
onset. The post-incident snapshot at 15:12:52 EDT showed a Dell U3223QE, no connected
dock, but a running hidden substrate and active caffeinate assertions. The local
evidence bundle is `/tmp/khudson-investigation-20261007.tar.gz`; the process/status
snapshot is `/tmp/khudson-runtime-20261007.txt`. These temporary files are not a
durable archive.

The changes above fix reproducible lifecycle, polling, state-loss, and observation
defects. They do not establish that khudson caused the system-wide input freeze.
The previous detector could miss this symptom because pointer motion reset its
aggregate idle measurement. There is no justified basis here for resetting TCC,
restarting WindowServer, or disabling an unrelated display utility.

After deployment, use the already-recorded input journal and launcher/bus/magicbus
logs for the incident interval. Compare key/click observations against motion,
then callback receipt against reconciliation outcomes and AppKit heartbeat.
Preserve unknown error codes as new harness fixtures before assigning recovery
logic. No action during the freeze is required.

## Durable actions and standalone conformance

The SQLite journal commits acceptance and compare-and-swap state transitions
before execution. It retains unresolved operations, caps total record count,
and prunes only older terminal outcomes. Deduplication applies within retained
history; clients must not retry arbitrarily old IDs. Restart discards pending
transient input, marks interrupted non-reconcilable work uncertain, and permits
only whitelisted absolute desired-state controls to reconcile. Raw pointer
motion is intentionally not persisted. The integrated client's delivery worker commits before transport; the bus claims
that same record before execution. An in-memory queue submission is not a commit.
[actionjournal](../khudson/internal/actionjournal/journal.go),
[bus recovery](../khudson/internal/bus/actions.go).

Tests exercise corrupted/future schemas, read-only failures, concurrent state
transitions, duplicate IDs, interrupted work, and a subprocess exiting without
closing/checkpointing its database. This establishes application recovery
semantics, not proof against physical storage failure.
[journal tests](../khudson/internal/actionjournal/journal_test.go).

The `component` check runs the production serial worker with fake providers and
executors. It covers a blocked provider's cancellation, no overlapping polls,
action commit before execution, rejection of unpublished actions, cancellation
and reaping, and retained data on poll failure. Rendering parity compares the
standalone body's output to integrated widget composition. No real displays,
HID devices, or input-posting APIs are used.
[engine tests](../khudson/component/engine_test.go),
[view parity](../khudson/surface/component_test.go).

Client diagnostic files retain two bounded generations and use a shared lock
for rotation across processes. Boundary events identify the executable and
process session; action transitions carry operation IDs. View-originated action records include view identity/generation; child effects
retain their parent operation identity. The Nix posture check also verifies
that couch configuration keeps RC disabled and that its startup session names
packaged standalone executables.
[file logging](../khudson/internal/diagnostic/file.go),
[events](../khudson/internal/diagnostic/diagnostic.go),
[posture check](../nix/posture-check.nix).

The HID registry check now distinguishes known module-local contract failures
from unclassified shared-runtime failures. A duplicate waiter disables its
module, preserves siblings, and records a terminal diagnostic. Unknown errors
still stop the daemon. Existing device-loss and transient feature retries stay
inside their device owners. Tests pin both sibling isolation and shared-runtime
shutdown, including the scanner join before hidapi teardown.
[module runner](../touchd/module.go),
[module lifecycle tests](../touchd/module_lifecycle_test.go).

## Terminal visibility and passive reader conformance

`internal/visibility` feeds report bytes through the pinned ultraviolet parser,
then through the same mode-report conversion used by Bubble Tea. Cases cover
support negotiation, duplicate reports, inherited mode ownership, unsupported
states, and focus independence. The adapter restores a mode only when it enabled
it. See the [protocol](https://rockorager.dev/misc/visibility-reports/),
[adapter tests](../khudson/internal/visibility/visibility_test.go), and
[kitty changelog](https://sw.kovidgoyal.net/kitty/changelog/).

The component tests advance fake time through a hidden day: no poll starts, an
in-flight poll joins, and showing the view restores exactly one cadence. Rapid
hide/show rejects the canceled generation's result. Keyboard tests inject
visibility reports and a fake USB reader; hiding performs no hardware query and
disarms an unconfirmed firmware action. These tests do not exercise native
window visibility delivery or perform a firmware update.
[Worker tests](../khudson/component/engine_test.go),
[keyboard test](../khudson/cmd/kuiboard/main_test.go).

Bus relay tests simulate ordinary and Edge clients, duplicate wakeups, final
consumer loss, reconnect, and shutdown. They prove one reader per demanded feed
and joined shutdown. Touch cancellation closes and joins a silent decoder;
recognizer cancellation forgets interrupted contacts without synthesizing a
tap, swipe, or long press. Key-feed EOF and malformed JSON both clear held state
and wait before reconnecting. No HID device is opened by these tests.
[Relay tests](../khudson/internal/bus/relays_test.go),
[gesture test](../khudson/internal/gesture/cancel_test.go),
[key-feed tests](../khudson/internal/keyfeed/feed_test.go).

Bus and launcher stdlib diagnostics now write bounded `bus.diagnostics.log` and
`hud-launcher.diagnostics.log` files under the state directory's `log/`. They
are separate from launchd's open stdout/stderr files, so rotation does not
compete with those descriptors. Each retains a current and previous file capped
at 4 MiB. Renderer transitions and standalone visibility transitions include
their local generation. Other boundary events carry process session identity;
semantic action records carry view identity/generation and parent operation IDs.
[Service logging](../khudson/main.go),
[renderer records](../khudson/internal/bus/renderer.go),
[visibility records](../khudson/component/engine.go).

Journal fullfsync and checkpoint_fullfsync settings are applied on every native
SQLite connection, including replacements after connection retirement. A test
forces new connections and reads the actual pragma values.
[Connection setup](../khudson/internal/actionjournal/journal.go),
[journal test](../khudson/internal/actionjournal/journal_test.go).

History restore uses `registry.Persistent()` rather than constructing all
providers. The bus registry now constructs a Reno client proxy. Only the separate
Reno owner constructs its stateful backend, after acquiring an exclusive file
lock and socket and recovering its journal.
The registry test checks that restored history and polled providers share the
same history instances.
[History setup](../khudson/main.go),
[bus startup](../khudson/internal/bus/bus.go),
[registry test](../khudson/internal/module/registry/registry_test.go).


## October 7 activation follow-up

The 19:56 EDT apply exposed a native startup defect: an already-prohibited
unbundled application could return false from a redundant activation-policy
setter. The adapter now reads actual policy before/after attempting a change.
The original log did not capture policy, so it cannot prove the native cause.
Eight Objective-C cases test the production helper with a fake application;
they never initialize NSApplication.
[Apple prohibited policy](https://developer.apple.com/documentation/appkit/nsapplication/activationpolicy-swift.enum/prohibited),
[setter contract](https://developer.apple.com/documentation/appkit/nsapplication/setactivationpolicy(_:)),
[helper and fixture](../khudson/internal/cgdisplay/testdata/application_policy.m).

The 20:09 EDT apply subsequently produced successful registration and advancing
AppKit heartbeat. The 21:19 EDT apply installed
`/nix/store/9r1ha3hp566xhfrm6lfdwpjlv82nsawr-khudson-0.1.0` for khudson and Reno.
Read-only inspection found Reno PID 25849 running and HUD-launcher PID 25854
recording heartbeat 1, 3, and 5 through 21:22:25, with the Edge absent and no
completed callbacks. Sources were installed store markers, `launchctl print`,
and `log/hud-launcher.diagnostics.log` under the runtime state directory.
This establishes startup and event-loop progress, not hotplug delivery, native
Quick Access visibility, Accessibility permission, or the input freeze's cause.
The user reported a later successful apply before the error-outcome and shutdown
review below. That later deployment was not inspected here; the final source
changes described below followed it.

## View, provider, and job-owner conformance

The current architecture is documented in [architecture-current.md](architecture-current.md).
These cases execute production state machines with substituted OS boundaries:

| Boundary | Scenarios and retained evidence |
| --- | --- |
| Public package dependency | `go list -deps` rejects standalone imports of integration bus, dock host, or view transport. All six standalone binaries are checked in the package. [Nix check](../../../../../lib/mkchecks.nix), [posture](../nix/posture-check.nix). |
| Integrated view sessions | Independent selections, demand union, hide/show lease retirement, cross-connection lease rejection, offscreen widget refusal, renderer/config invalidation. [Tests](../khudson/internal/bus/views_test.go). |
| Durable delivery | Commit precedes wire send; bus claims a precommitted action once; stale intent is explicitly discarded; parent and view correlation survive child-effect persistence. [Transport tests](../khudson/internal/viewtransport/connection_test.go), [bus journal tests](../khudson/internal/bus/actions_test.go). |
| Standalone worker | No overlapping polls, joined cancellation, hidden-day quiescence, stale poll and queued action rejection across hide/show. [Tests](../khudson/component/engine_test.go). |
| Keyboard reads | Background loader ownership, hidden cancellation, late-result rejection, canceled identity reads do not poison the next lifetime. Early key state is bounded by valid matrix slots and cleared on source loss. [Watcher](../khudson/internal/keyboard/watcher_test.go), [identity](../khudson/internal/keyboard/usbserial/usbserial_test.go), [pending keys](../khudson/internal/keyboard/kbview/pending_test.go). |
| Host commands | Close cancels and joins commands; commands delivered after terminal exit cannot start. [Tests](../khudson/internal/taskgroup/group_test.go). |
| Firmware jobs | Exclusive worker, persisted submission, duplicate refusal, worker disappearance, spawn/verification failures, explicit uncertainty discard. Fake executor only; no firmware is flashed. [Tests](../khudson/internal/keyboard/flashjob/job_test.go). |
| Reno shared owner | Competing owner cannot construct a backend, lost acknowledgement cannot repeat transition/effects, interrupted work becomes uncertain without replay, inspection/discard, invalid request, idle-client shutdown, lock release. Temporary sockets and fake backend/executor only. [Tests](../khudson/internal/renoservice/service_test.go). |
| Reno display cache | Repeated polls reuse snapshot; invalidation survives failed query and retry backoff; no subscription uses bounded fallback; fake application loop handles worker failure and premature exit. [Cache tests](../khudson/internal/cgdisplay/cache_test.go), [host tests](../khudson/internal/cgdisplay/cache_test.go). |
| Error identity | Wrapped native failures retain numeric codes and separate occurrence counters even when their messages match. Keyboard cache/generation/identity failures name separate boundaries. [Tests](../khudson/internal/diagnostic/diagnostic_test.go), [loader](../khudson/internal/keyboard/loader.go). |

Reno inspection uses `renoman jobs [after-sequence]` and `renoman discard-job ID`.
Bus inspection uses `khudson ctl actions [after-sequence]`. Both socket APIs return
`{"records":[...],"next":N}`; `next` is omitted on the final page. Pages retain full
payloads and cap encoded data at 1 MiB and 128 records. Follow the cursor to inspect
older/larger histories; these are live pages, not a transactional snapshot. Firmware
inspection uses `kuiboard --flash-status` and `kuiboard --discard-flash ID`.
An uncertain result requires review; neither command family automatically repeats
an ambiguous side effect. The new signed Reno executable can require its own
macOS grants. Routine checks do not exercise native permission prompts or install
agents. The activation check executes the production retirement helper against fake
launchctl/process boundaries and checks rendered artifacts. Live handoff of
the revised activation still requires deployment observation.

## Ecosystem review regressions

| Boundary | Checked behavior |
| --- | --- |
| Visibility | A new visibility generation cannot act on a pre-hide snapshot. Provider errors back off up to 30 seconds, without accelerating slower configured cadences. Success restores normal cadence. [Worker tests](../khudson/component/engine_test.go). |
| Owner lifetime | Competing starters cannot acquire the owner lock; listener closure does not release ownership while workers are retiring. Unknown socket-holder probe failures preserve the socket. [Lock tests](../khudson/internal/ownerlock/ownerlock_test.go), [socket claim tests](../khudson/internal/sockclaim/sockclaim_test.go). |
| AX boundaries | Failed timeout setup retries; native error codes remain typed, partial hiding is an error, unknown visibility is not hidden, and exhausted raises preserve failures. Optional missing attributes do not swallow messaging errors. [AX tests](../khudson/internal/ax/ax_test.go). |
| Transport | Oversize input/output, byte-budget exhaustion, truncated NDJSON, stalled peers, and terminated oversize kitty responses. [Socket tests](../khudson/internal/socketio/decoder_test.go), [writer tests](../khudson/internal/socketio/writer_test.go), [RC tests](../khudson/internal/rc/rc_test.go). |
| Diagnostics | Contended log locks return immediately; subsequent writes account for loss. Joined failures emit each native code. [File tests](../khudson/internal/diagnostic/file_test.go), [classification tests](../khudson/internal/diagnostic/diagnostic_test.go). |
| Reno children | Replacements wait for previous drivers; shutdown joins retiring children and idle watchdogs. Effect admission rechecks generation under the spawn lock. [Driver tests](../khudson/internal/module/reno/sustain_test.go), [watchdog tests](../khudson/internal/module/reno/armwatch_test.go), [effect test](../khudson/internal/effectrun/admission_test.go). |
| Command execution | Output/pipe lifetime bounded; cancellation reaps command groups. Signals and interrupted execution remain uncertain. [Subprocess tests](../khudson/internal/subprocess/output_test.go), [outcome tests](../khudson/internal/actionjournal/outcome_test.go). |
| Persistence | Concurrent writes publish complete files and remove temporary files on failure; firmware history cannot overwrite an existing generation. [File tests](../khudson/internal/atomicfile/write_test.go), [generation tests](../khudson/internal/keyboard/generations/generations_test.go). |
| Hardware transport | Idle disconnect and shutdown join clients; queue overflow disconnects lagging consumers. Cancelled/closed HID requests write nothing and short writes remain errors. [Broadcaster tests](../touchd/main_test.go), [HID tests](../touchd/logiretch_test.go). |
| OBS process detection | A failed process probe prevents config mutation rather than being treated as absence. [Test](../khudson/internal/renocontrol/obsini_test.go). |
| Activation | Loaded, absent, failed bootout, non-exiting owner, and dry-run cases execute the same retirement helper used by activation. No native agents are driven. [Harness](../nix/agent-lifecycle-test.sh). |

Apple documents that [AX timeout setup](https://developer.apple.com/documentation/applicationservices/1459345-axuielementsetmessagingtimeout)
can fail, and that [AX messaging errors](https://developer.apple.com/documentation/applicationservices/axuielement_h)
include unresponsive peers. These codes do not justify assuming TCC denial.
The adapter checks setup and preserves codes; retry/backoff and partial-result
handling are khudson policy, verified with fake boundaries.

## Cross-component conformance

These tests cover production callers and recovery across component boundaries:

| Finding | Fix and regression coverage |
| --- | --- |
| Reno service never called backend Close | Service shutdown joins backend cleanup before releasing its lock or journal; close failures propagate. The test runs the actual service and attempts a competing owner during blocked cleanup. [Service tests](../khudson/internal/renoservice/service_test.go). |
| Socket deadlines did not cancel backend polls | Request contexts have a deadline; disconnected poll clients cancel their work. Waiting for a busy backend honors cancellation. Virtual-time tests drive the actual request handler. [Service tests](../khudson/internal/renoservice/service_test.go). |
| Bus interruption used the old failed classification | Started actions interrupted by timeout or signals retain uncertain outcomes, using the same classifier as standalone execution. [Bus action tests](../khudson/internal/bus/actions_test.go). |
| Journal inspection could exceed the client frame limit | Cursor pages cap both encoded bytes and record count. The service transport test inspects large HTML-escaped payloads across every page without truncation or duplicates. [Service tests](../khudson/internal/renoservice/service_test.go). |
| Recovery could overwrite a newer desired state | Superseded intent is discarded; completed successor evidence survives pruning while needed. Concurrent global desired-state controls follow acceptance order. [Recovery tests](../khudson/internal/bus/actions_test.go), [pruning test](../khudson/internal/actionjournal/journal_test.go). |
| HUD colors duplicated an unbounded kitty reader | Color commands share the bounded RC transport; the color-only API rejects input commands. A fake socket sends an oversized complete response through the HUD client. [HUD test](../khudson/internal/bus/theme_test.go), [RC tests](../khudson/internal/rc/rc_test.go). |
| Layout publication ignored persistence failure | Failed persistence refuses publication. The test makes the destination unwritable as a file and checks the current layout remains unchanged. [Layout tests](../khudson/internal/bus/layoutstate_test.go). |
| Control success ignored final journal failure | Lost outcome persistence returns an operation ID and uncertainty. The test closes storage after the semantic operation succeeds. [Action tests](../khudson/internal/bus/actions_test.go). |

## Error outcomes and shutdown conformance

P2 denotes a reliability or recovery defect; P3 denotes a lower-impact API or
diagnostic defect. All listed defects have regression coverage.

| Priority | Boundary and fix | Regression coverage and cost |
| --- | --- | --- |
| P2 | Lost kitty/Reno replies, mismatched response IDs, and partial effect dispatch retain uncertain outcomes through the bus journal. | Fake peers close or send malformed/wrong acknowledgements; rejected effects cannot repeat provider transitions; partial chain admission retains required cleanup for review. Uncertain operations require inspection and consume retained journal space. [RC](../khudson/internal/rc/rc_test.go), [service](../khudson/internal/renoservice/service_test.go), [bus](../khudson/internal/bus/actions_test.go). |
| P2 | Reno encoding and final-commit errors preserve operation identity/state; provider errors retain their diagnostic boundary. | Invalid floating-point responses and storage closure after a mutation exercise real serialization and persistence boundaries. Read failures do not become uncertain mutations. [Service](../khudson/internal/renoservice/service_test.go). |
| P2 | Shutdown fences provider transitions, joins the event-loop stop worker, and prevents a cancelled caffeinate owner from recovering or spawning children. | Closed-host transitions, blocked stop completion, deterministic normal-stop ordering, and pre-cancelled supervisor tests. Shutdown waits for an admitted transition before closing the effect host. [Effect host](../khudson/internal/effectrun/admission_test.go), [application host](../khudson/internal/cgdisplay/cache_test.go), [caffeinate](../khudson/internal/bus/caffeinate_test.go). |
| P2 | Failed theme application and palette reads retain desired state and pending reconciliation. | Failed host calls recover through the adoption path. Semantic selection can precede actual host colors. [Theme](../khudson/internal/bus/theme_test.go). |
| P2 | Layout persistence releases the global bus lock and revalidates config/views before publication. Post-publication sync failures carry a distinct uncertain outcome. | Blocked persistence leaves bus state accessible; config invalidation refuses publication. Injected directory-sync failure verifies the file is already visible. [Layout](../khudson/internal/bus/layoutstate_test.go), [atomic publication](../khudson/internal/atomicfile/write_test.go). |
| P2 | Display-name, USB identity, Dock PID, process identity, brightness, and kitty helper calls share bounded output and pipe-drain handling. Owner files use atomic publication. | Cancellation, process-group cleanup, output overflow, typed exits, and explicit timeout tests. Overlarge output is rejected; existing ten-second display/USB/kitty limits remain explicit. [Subprocess](../khudson/internal/subprocess/output_test.go), [kitty controls](../khudson/internal/claudecontrol/verbs_test.go). |
| P3 | Empty kitty commands return errors; AppleScript errors retain exit identity. Input-stall probes report failed console/process reads and bound diagnostic output. Explicit journal discard is observable. | Fake commands exercise empty arguments, stderr/exit preservation, cancellation, and diagnostic output overflow without querying the host. Diagnostic snapshots over 1 MiB are rejected. [Probe tests](../touchd/wedge_test.go). |

The log-rotation fixture creates both retained generations before racing clients;
nonblocking lock contention can legitimately drop most concurrent writes. The Nix
check repeats that race test 25 times and retains `diagnostic-rotation.txt`.
[Rotation test](../khudson/internal/diagnostic/file_test.go).

## Validation artifacts

Completed on October 8, 2026 (EDT), including the error-outcome and shutdown
fixes above and 20 additional top-level regression tests in this review loop.
Both Go modules passed vet, owned-source formatting, and all-package race tests.
Counts below are top-level tests, excluding subtests:

| Check | Result | Artifact |
| --- | --- | --- |
| khudson | 1,045 passed, 18 skipped; 55 packages passed, 11 had no tests | `/nix/store/fqjx901bw9mhlsl12vx7k4xl88xwxjyc-khudson-display-conformance-0.1.0` |
| magicbus | 86 passed, no skips | `/nix/store/bccs5vlsb28g700x73pzib00zx7ldjgj-magicbus-conformance-0.1.0` |
| Posture and activation | Passed; five owner-retirement scenarios and rendered configuration assertions | `/nix/store/xk3gzp6lqz48l3s81250yx62wa8gmsqw-khudson-posture-check` |
| Signed installer | Passed | `/nix/store/09iga82q9vr1zqblvqx8va18ss7y8164-khudson-install-check` |
| Full system build | Passed, including package tests and activation dependency ordering; not applied | `/nix/store/9bfd3c5fn53xicn7575c9qmgw5gdcq4r-darwin-system-26.11.4cff07d` |

The khudson artifact also contains eight passing Objective-C activation-policy
cases and the standalone dependency listing. Both Go artifacts retain
`conformance.jsonl` with individual test results and skip reasons. Including
subtests, the logs contain 1,295 passing test events and 18 skipped test events.
The separate 25-run log-rotation check also passed; repetitions are excluded
from these counts.

Skips cover opt-in AX, GUI kitty, OBS, live host/session/layout fixtures,
unavailable native command-line tools, and an absent keyboard. These are
explicit coverage limits, not successful native integration tests. The checks
establish neither the original freeze's cause nor real callback delivery,
permission grants, or live service handoff after deployment.

The reviewed package is
`/nix/store/6gl8xkw0cz3scffvjc5w4i1fsla0nm1k-khudson-0.1.0`.
An October 16 read-only audit found the system artifact above at
`/run/current-system`, with both installed khudson and renoman store-path
markers pointing to this package. The October 8 build has since been applied.
`khudson ctl status` responded with no docks, touch device, or main-kitty socket;
`renoman jobs` returned no records. This confirms service reachability, not the
hardware and GUI behaviors excluded above. The October 8 build log is
`/tmp/khudson-loop-result.log` on the review host.

## October 16 review

Shared-owner actions now retain the distinction between definite refusal and
lost delivery in both the bus and standalone component host. Reno mutations
canceled before the backend runs are failed; a failed journal claim remains
uncertain because another request may already be executing that operation.
Regression coverage includes an absent owner, refused and lost replies,
concurrent duplicate requests, and a forced SQLite claim failure.
[Bus outcomes](../khudson/internal/bus/input.go#L122),
[component outcomes](../khudson/component/engine.go#L268), and
[owner claim/outcome handling](../khudson/internal/renoservice/service.go#L285).

The daily kitty overlay now preserves its LaunchServices marker while adding
PATH. Losing the marker caused kitty to skip its launch arguments and home
working directory. The copied HUD bundle deliberately omits that marker so it
cannot consume the daily terminal's socket arguments. The posture check pins
the daily bundle marker and exact socket arguments.
[Overlay](../../../../../modules/darwin/kitty-overlay.nix#L77),
[HUD bundle](../nix/hud-app.nix#L95), and
[posture check](../nix/posture-check.nix#L43).

Current conformance artifacts:

| Check | Result | Artifact |
| --- | --- | --- |
| khudson | 1,050 passed, 18 skipped; race detector and vet passed | `/nix/store/hk811x6qd74k7wl3n0dm9v50ygj0qy6p-khudson-display-conformance-0.1.0` |
| magicbus | 86 passed, no skips; race detector and vet passed | `/nix/store/yj0pxp3vb30yrjmpvq4zygxh1vfgf8rd-magicbus-conformance-0.1.0` |
| Posture | Passed, including bundle marker, socket arguments, and five owner-retirement scenarios | `/nix/store/ch6g05s3l4b4mp7nkx3bf826my50l4xb-khudson-posture-check` |
| Signed installer | Passed | `/nix/store/alp89zhs73561hhpr4c2gzvgmx4xlpis-khudson-install-check` |
| Kitty startup and prompt | Startup session and Cmd+N resolve to ~/dev; directory icons and prompt character exist in FiraCode Nerd Font Mono | `/nix/store/6qr63vlavi5q75y8q3bk4vhnyzjbrrsw-kitty-startup-check` |
| Full system | Built; activation requires a sudo password | `/nix/store/p7mhs2jiy26fpc30cp3jmvcf54fjf2fb-darwin-system-26.11.4cff07d` |

The khudson artifact also contains eight passing Objective-C activation-policy
cases and the passing 25-run diagnostic-rotation check. Hardware and GUI skips
retain the limits described above; the original input-freeze cause remains
unproven. All 15 selected flake checks passed, including Codex's 10 TOML-update
fixtures, Claude harness checks, zsh startup, HUD glyphs, and krib sheet exports.
Noninteractive activation stopped with `sudo: a password is required`; these
October 16 changes have not been activated. The daily kitty was not restarted.
