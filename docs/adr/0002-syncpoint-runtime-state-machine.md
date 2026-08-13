# ADR 0002: Sync-point runtime state machine

- Status: Accepted
- Date: 2026-08-13

## Context

The Go-native SUT exposes named points where a worker can pause inside a
transactional workflow. Fixture-specific barriers can coordinate those points,
but each barrier defines its own arrive, wait, release, cancellation, and
teardown behavior. The engine therefore has no reusable contract for targeting
one worker at one point or for distinguishing a protocol error from an expected
wait.

Coordination also needs to remain independent of MySQL. A worker that has not
reached an expected point may be waiting in the database, delayed by the
scheduler, or completing through another branch. The coordination primitive
cannot determine which cause applies.

## Decision

Use one in-process runtime for one schedule execution. A coordinator registers
each worker before invocation, waits for named arrivals, releases an exact
worker and point, and reports the worker's terminal result with `Finish`. A
runtime is closed after that execution and is not reset or reused.

The runtime records these worker states:

| Current state | Operation | Result |
| --- | --- | --- |
| `running` | `Arrive(point)` | `arrived(point)`; the caller blocks |
| `db_blocked(point)` | `Arrive(point)` | `arrived(point)`; the caller blocks |
| `db_blocked(p1)` | `Arrive(p2)` | point mismatch error |
| `arrived(point)` | `Release(point)` | `released(point)`; `Arrive` returns |
| `arrived(p1)` | `Release(p2)` | point mismatch error |
| `released(p1)` | `WaitArrive(p1)` | invalid transition error |
| `released(p1)` | `WaitArrive(p2)` | wait for the next point |
| `running`, `db_blocked`, or `released` | `Finish(nil)` | `done` |
| `running`, `db_blocked`, or `released` | `Finish(error)` | `failed` with the cause preserved |
| `done` or `failed` | `Arrive`, `Release`, or `Finish` | invalid transition error |
| any nonterminal state | `Close` | blocked arrivals and waits return a closed error |

Release is targeted: only the registered worker currently blocked at the exact
point can advance. Arrive-before-register, release-before-arrive, mismatched
points, duplicate release, and terminal-state mutations are errors. A worker
may have at most one active `WaitArrive`; a concurrent wait or a wait for an
already released point is also an error.

`WaitArrive` has a caller context and a separate runtime timeout. Caller
cancellation returns the context error without changing the worker to
`db_blocked`. If the runtime timer expires before the expected arrival, the
runtime returns `Timeout` and records the nonterminal, timeout-inferred
`db_blocked(point)` state. That state is an inference about lack of progress,
not a database lock detector. The same worker can later arrive at that point
and continue. A timeout wake rechecks state while holding the runtime mutex, so
an arrival already recorded wins over the timeout.

If an `Arrive` context ends before release, the active arrival is invalidated,
its point is cleared, and `Arrive` returns the context error. Release and
cancellation are serialized by the runtime mutex: whichever transition is
recorded first wins. The runtime does not turn cancellation into a terminal
failure.

Terminal state belongs to the coordinator that consumes worker results. It
calls `Finish(workerID, nil)` for `done` or `Finish(workerID, err)` for `failed`.
The runtime neither invokes workers nor consumes adapter result channels.

`Close` is idempotent. It wakes blocked `Arrive` and `WaitArrive` callers with a
closed error and rejects new registration, arrival, wait, and release
operations. It does not manufacture terminal states; already registered
workers may still be finished by their coordinator during teardown.

The implementation uses one mutex, a generation-based change notification
channel, and one release channel per active arrival. It does not poll or start a
goroutine for each wait.

## Consequences

- Fixtures share strict worker-and-point coordination semantics instead of
  defining a new barrier contract for every workflow.
- Targeted release can hold one worker while another remains blocked at the
  same point.
- Protocol mistakes fail immediately instead of being reported as timeouts.
- Timeout inference can be a false positive for database blocking. Tests that
  need database evidence must collect it separately, as the matching fixture
  does with the current InnoDB row-lock wait count.
- Coordinators must register workers before invocation, forward every terminal
  result to `Finish`, and close the runtime during teardown.
- The runtime does not save, enumerate, or independently replay schedules. It
  also does not provide an invariant Oracle, an HTTP wire protocol, or a
  database-specific lock detector.

## Alternatives considered

### One global barrier

A global barrier can release a group after every member arrives, but cannot
target an exact worker and point or represent a worker that has not reached the
barrier. It was rejected because matching needs different release orders for
the vulnerable and locking paths.

### Automatic release after arrival

Automatically releasing a worker when a condition is met reduces explicit
schedule code, but hides the transition that determines the interleaving. It
also makes duplicate and stale release errors invisible. Explicit targeted
release keeps that decision with the coordinator.

### Polling and sleeps

Polling snapshots or sleeping between worker starts makes progress depend on
timing and consumes the same timeout budget for observation and coordination.
Channel notification and explicit release were selected so normal progress is
event-driven and timeout retains its inference meaning.
