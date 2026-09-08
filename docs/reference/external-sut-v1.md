# External SUT wire contract v1 (proposed)

This is planned v0.2.0 work from [ADR 0010](../adr/0010-external-sut-protocol.md),
not an available adapter or configuration format. Normative words describe what
future implementations must do. Artifact versions and wire versions are
independent. [Shared conformance cases](external-sut-conformance.md) accompany
this contract.

## Transport, framing, and version

The Go engine peer (E) owns one local child JVM peer (J). E writes child stdin;
J writes stdout. Every frame is a four-byte unsigned big-endian byte length,
followed by exactly that many bytes of one UTF-8 JSON object. Length must be
1–1,048,576 bytes. Read partial headers/payloads until complete; never assume a
pipe read equals a frame. Reject invalid UTF-8, duplicate JSON keys at any
level, trailing JSON values, unknown fields/types, and invalid required fields.
Zero/oversized lengths fail before allocating the declared buffer. EOF in a
header/payload is fatal; EOF on a frame boundary is also fatal except during
verified normal shutdown. Flush each frame. Stdout banners or log text are
protocol corruption.

Every message has exactly `v`, `type`, `run`, `session`, `seq`, and `body`.
`v` is the integer `1`; there is no version fallback or downgrade. An incompatible
version fails startup/session without running a command. `type` selects the
closed body shapes below. No field is optional unless explicitly stated; null
is invalid except `terminal.error`. Integers have no fractional/exponent form.
Strings must contain valid Unicode scalar values. Names are nonempty, at most
128 UTF-8 bytes, and contain no control characters or leading/trailing whitespace.
Parameter maps have name keys and string values; frame size bounds their size.

The enabled child bootstrap reserves stdout and reads `start` before creating
its Spring context/DataSource. Its control reader remains available during
initialization so Stop can cancel startup. E starts the startup deadline before
launch, bounded by both its configured startup budget and the remaining Start
context; `startup_ms` conveys only the remaining budget to J. No command can
run before E validates ready. Startup failure sends fatal when possible; Stop
during startup may produce stopped without ready if cleanup is proven.

## Identity, order, and duplicate handling

| Field | Meaning and scope |
| --- | --- |
| `run` | E-generated 32 lowercase hex characters; correlation across one replay/exploration operation, independent of artifact run IDs. Never a release authority. |
| `session` | E-generated fresh random 128-bit value as 32 lowercase hex characters per adapter/schedule execution, including each repeat. One child serves exactly one session. |
| `invocation` | E-generated 32 lowercase hex characters, never reused in a session. Bound immutably to one worker and command. |
| `worker` | Scenario worker name. Unique among live invocations; reusable only after prior terminal acceptance and Go result channel closure. |
| `arrival` | Positive decimal string without leading zeros, starting at `"1"` and increasing by one per invocation, maximum `"100000"`. |
| `point` | Registered sync-point name; compared exactly, without case folding. |
| `seq` | Integer 1–100000, increasing by one independently in each direction, starting with start/ready (or startup fatal). Covers all message types. |

A release targets `(run, session, invocation, worker, arrival, point)`, never
just a worker or point. Each invocation has at most one outstanding arrival.
The bridge binds a private Go context and runtime client to that session; it
never reassigns a queued message or bridge task to a new runtime.

Validate framing, schema, and version first. A well-formed foreign run/session
frame on an established session is dropped without touching current sequence
state or the runtime (record only a sanitized stale-message counter). The child
binds its identity from its first valid `start`. It may not rebind. The engine
already knows the expected identity before reading ready.

Within the current session, process messages in sender order. Retain a digest
of each accepted frame payload until Stop; an already-seen sequence with exactly
the same payload bytes is ignored without repeating effects or replies. Different
bytes for an existing sequence, a gap, or an unseen lower sequence is fatal.
No reconnect, resend timer, automatic invocation retry, or reply replay exists
in v1: pipes are reliable while alive and ambiguity ends the session. This
bounded duplicate rule only protects against accidental duplicate writes.
Senders must fail instead of wrapping the sequence limit.

After sequence validation, unknown invocation/worker, changed immutable binding,
unknown command/point, wrong direction, and invalid state are fatal. Retain
completed invocation tombstones until Stop: a late `arrive`, `release`, `cancel`,
or identical terminal body for a retired invocation is consumed with no effect;
a conflicting terminal body is fatal. An old invocation must never affect a
new invocation using the same worker. For a live invocation, a release for an
already-released arrival is ignored only if its full identity matches a recorded
release. Future arrival numbers, changed points, release before arrival, and
second concurrent arrivals are fatal. A semantic duplicate `invoke` or `start`
under a new sequence is fatal; only exact sequence duplicates are ignored.

## Message catalog

The `I` body fields below mean `invocation` and `worker`. `A` means `I` plus
`arrival` and `point`. These are notation for the table, not nested wire fields.

| Type | Direction | Exact body fields | Required behavior |
| --- | --- | --- | --- |
| `start` | E → J | `variant` (name), `params` (string map), `commands` (distinct name array), `points` (distinct name array), `capacity` (integer 1–1024), `database` (object below), `startup_ms`, `cancel_ms` (positive integers ≤ 2147483647) | First E frame. Configure application, validate registration and transaction profile, establish pool, and ping the fixture before readiness. |
| `ready` | J → E | `commands`, `points`, `capacity` | First J frame on success. Exactly echo the validated start arrays/order and capacity; DB probe lease returned, application initialized, no worker running. E verifies equality before Start returns a Handle. |
| `invoke` | E → J | `I`, `command` (name) | Only after ready; reserve worker and invocation, enqueue one worker. Uses start's variant/params. Reject excess capacity, unknown command, or an already-active worker. |
| `accepted` | J → E | `I` | Reserve invocation before sending; precedes all arrivals/terminal for it. Acknowledges dispatch, not transaction completion. E may return a result channel from Invoke before receiving this. |
| `arrive` | J → E | `A` | Worker gate is installed before sending. E validates identity, then calls its session's `Client.Arrive` once in an independent bridge task. |
| `release` | E → J | `A` | Send only after that bridge call returns nil while the invocation is still uncanceled. J wakes only the gate with the exact identity. Error or cancellation from Arrive never authorizes release. |
| `terminal` | J → E | `I`, `transaction`, `connection`, `error` | Only after accepted, proxy exit and verified cleanup; no outstanding arrival. One terminal per invocation. Rules below. |
| `cancel` | E → J | `I`, `reason` (`context` or `stop`) | Cancel one invocation, including one awaiting accepted. Wake its gate with an exception, request JDBC cancellation, and await transaction cleanup. This is not release or completion. |
| `stop` | E → J | `budget_ms` (positive integer ≤ 2147483647) | Close admission permanently, cancel all active invocations, await cleanup, close pool and application. Valid during startup as well as after ready. |
| `stopped` | J → E | empty object | All invocation terminals sent, no worker/lease remains, pool/application closed. Flush, close stdout, and exit 0. Cannot be sent after fatal. |
| `fatal` | Either | `kind`, `message` | Session failure, never a worker terminal. `kind` is `version`, `protocol`, `startup`, `transport`, `transaction`, `cleanup`, or `shutdown`; message is sanitized, at most 1024 UTF-8 bytes. Close admission and begin bounded cleanup. Best effort only if writing remains possible. |

`database` has exactly `driver` (`mysql`), `host` (nonempty string), `port`
(integer 1–65535), `name`, `username`, and `password` (strings; name/username
nonempty). It describes the fixture's application account and host-mapped port,
not a Go DSN or JDBC URL. The child constructs a JDBC URL with correctly encoded
components and supplies credentials separately. The launch profile uses only
this ephemeral local test database; it does not accept arbitrary JDBC options.

`terminal.transaction` is `committed`, `rolled_back`, or `not_started`;
`connection` is `returned` or `not_acquired`. `not_acquired` is legal only with
`not_started`; all other outcomes require `returned`. `not_started/returned`
covers a failed transaction begin after a lease was acquired. Unknown outcome
or uncertain cleanup is fatal, with no terminal for that invocation.

`error` is null only for `committed/returned`. Otherwise it is an object with
exactly `kind` (`application`, `mysql`, `cancelled`), `message` (sanitized string,
≤1024 UTF-8 bytes), `mysql_code` (integer 0–65535), and `sql_state` (string).
For `mysql`, code is nonzero and SQLSTATE is five uppercase ASCII letters/digits;
otherwise code is 0 and SQLSTATE is empty. A committed transaction may carry an
error, for example an exception after commit; report the observed outcome.
Explicit rollback without an application exception still needs an application
error indicating rollback. Wire kinds are not diagnostic codes or verdicts.

E emits exactly one `WorkerResult` then closes its channel only for a validated
terminal after all invocation bridge tasks have unwound. Null error maps to nil;
non-null maps to an error, preserving MySQL vendor metadata (1213 deadlock,
1205 ordinary error) and cancellation identity. E measures Duration locally
from dispatch to terminal receipt; it is volatile and not sent on the wire.
Adapter-wide faults follow ADR gap G2, never `WorkerResult{Err:nil}` or an ordinary
worker error that would permit an oracle verdict to stand as a successful run.

## Cancellation, failures, and bounded stop

Cancellation is irreversible. The Go adapter cancels the invocation's bridge
context and sends `cancel`; Java atomically records cancellation, wakes any
sync-point gate exceptionally, and prevents future gates/commands from starting.
A racing release cannot clear that flag; a release received for a canceled gate is consumed without resuming it. A bridge task must recheck cancellation before queuing release, even after a nil runtime return. The command must allow the cancellation
exception to cross its proxy boundary under rollback rules. JDBC cancel/interrupt
is a request, not proof that a driver or server has stopped. A transaction already
committed can truthfully return `committed`; the canceled Go operation still
returns its context error. Do not rewrite its database outcome as rollback.

Any non-cancellation error from the runtime bridge is a fatal protocol error; it is never converted into release or a successful terminal.

`cancel_ms` is a local cleanup grace from cancellation receipt; it neither
extends the Go operation nor coordinates workers. Failure to confirm cleanup
within it becomes fatal and triggers process termination. A canceled outstanding
arrival is retired on both peers before terminal acceptance; E waits for its
canceled bridge call to unwind. If Java has sent an arrival that E has not yet
read when cancellation starts, E consumes it in the canceled invocation without
calling the runtime. Cancellation remains allowed until terminal retirement;
a later cancel is consumed by the tombstone rule.

Stop uses a cleanup context independent of the canceled run. E sets a single
absolute stop deadline from that context, sends the remaining `budget_ms`, and
reserves the final half of the initial remaining budget for forced termination
and reaping. At the halfway deadline it kills the child if graceful completion
is unproven; it never grants a fresh budget per phase. Writes, draining stderr,
waiting for workers, child exit, and reaping all respect the same deadline.
Stop is idempotent; repeated calls retain the original failure and cannot
restart the child. An expired budget returns an error promptly. The supported
child must not spawn descendants; if implementation allows any, it must own and
terminate the whole process tree under this same bound.

Either peer losing its control stream cancels all local invocation contexts
and wakes gates exceptionally. On coordinator loss, J starts a watchdog bounded
by `cancel_ms`, attempts rollback and pool cleanup, and terminates itself even
if a driver cannot be interrupted. Forced exit is never evidence of a known
transaction outcome. E independently supervises child exit and quarantines the
fixture after unproven cleanup.

Normal Stop requires `stopped`, stdout EOF, exit 0, no active worker/lease, and
no latched session fault. EOF before stopped (even exit 0), nonzero exit, broken
pipe, stalled writes/reads at the governing deadline, malformed messages, or
process death latch a run error. A silent live peer is bounded by startup/run/
stop deadlines, not by a heartbeat or a database-blocking inference. Fatal after
all terminals still invalidates run success; fault delivery must stay active
through oracle evaluation and Stop (G2). Failed or unproven cleanup quarantines
the fixture (G3); killing the JVM does not prove server-side rollback is complete.

## Provisioning, reset, logs, and evidence

E provisions and resets migrations/seed before starting J. G1 must supply the
application connection descriptor from that same prepared fixture. J disables
schema creation, migration runners, web listeners, scheduled/background DB
writers, and health jobs that mutate the database. The worker executor and pool
have at least the declared capacity; unrelated application work cannot consume
worker leases. Start fails if any declared command/point or transaction profile
is unsupported. Readiness means the context is initialized and a DB probe has
completed and returned its lease; it is not a log-line match or a fixed delay.

After all worker terminals, existing Go oracles query committed state. Stop
closes the JVM pool and reaps the child before another Reset. Successful Stop
proves no client work remains; failed cleanup requires teardown/reprovisioning,
not an attempt to race DROP DATABASE against unknown old transactions. New
sessions never inherit sockets, command state, tombstones, or runtime arrivals.

Start sends credentials only through the owned stdin pipe. Do not put them in
argv, environment variables, temporary config, traces, report artifacts, or
exceptions. Reserve stdout before Boot starts; disable the banner and route all
logging to stderr. E continuously drains stderr with a bounded 1 MiB tail;
overflow discards oldest log bytes without blocking a worker. Raw logs and start
frames are not persisted by default. Any explicit future log export must redact
credentials/JDBC URLs, use private file permissions, and remain separate from
normalized evidence. Error messages carry only sanitized summaries, no stack
traces, SQL literals, or raw driver connection strings. These rules assume an
explicitly instrumented trusted test application, not isolation from hostile code.

The wire carries execution facts only. Session IDs, counters, pipe ordering,
process timings and log text are excluded from deterministic fingerprints.
The orchestrator continues to infer `db_blocked` from its existing WaitArrive
behavior; Java sends neither that state nor an oracle verdict. Transport latency
can influence timeout observations, so the future repeated integration checks
must demonstrate stable evidence without weakening timeouts or using sleeps.
