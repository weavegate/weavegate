# Adapter outcomes and run finalization

The Go contracts in [`internal/sut`](../../internal/sut/sut.go) and
[ADR 0014](../adr/0014-adapter-outcome-boundaries.md) distinguish invocation facts
from session failure and operation cancellation. These contracts are implemented
for the Go-native adapter and orchestrator. The external wire adapter and Java
implementation remain planned.

## Invocation lifecycle

`Handle.Invoke` returns either a synchronous error with no stream, or one
`InvocationOutcome` stream. A valid stream publishes exactly one outcome and
closes. The outcome has exactly one non-nil field:

| Field | Required facts | Coordinator behavior |
| --- | --- | --- |
| `Worker` | Command transaction committed or rolled back; worker connection returned. | Preserve WorkerResult, call runtime Finish, and retain the existing worker-error classification. |
| `Unstarted` | Command transaction never began; acquired resources returned; non-nil cause and matching worker ID. | Retain UnstartedResult separately, abort execution with its cause, and never call Finish or invent a terminal. |

Go-native connection acquisition is asynchronous. Input validation, unknown
commands, active worker-ID conflicts, and already-canceled contexts are
synchronous rejections. Connection acquisition failure or cancellation before
command acceptance produces an unstarted outcome after Invoke returns.
Cancellation observation and command acceptance share a worker-local serialized
transition immediately before the command call. If cancellation wins, the
command is not called. If acceptance wins, later cancellation reaches the
running command through its context and the completed transaction is represented
by a WorkerResult. The adapter holds the worker reservation until cleanup and
stream closure, then permits reuse.

Unknown transaction outcome or unproven resource cleanup requires a session
fault; neither outcome type can represent it. A fault is latched before closing
an affected stream without an outcome. Empty streams, multiple outcomes,
malformed identities or outcome variants, and streams left open at cleanup are
protocol errors. Worker completion can advance runtime coordination before
stream closure, but oracle evaluation waits for all streams to close.

## Session fault and cancellation observation

Every started Handle exposes `SessionFaults`: a notification channel and a typed
`SessionFault` retaining its cause. `FaultLatch` supplies a concurrency-safe
implementation with a usable zero value. The first non-nil failure closes the
notification channel and remains visible to all current and late observers.
Subsequent failures do not replace it. Adapters and observers must not mutate a
published fault or cause. Successful Stop completes fault publication; failed
Stop cannot prove cleanup or rule out later failures.

Run observes faults through execution, provisional oracle evaluation, and Stop.
A fault cancels the execution/evaluation context with its original cause.
Evaluators must honor their context. Run also checks the latch synchronously,
so an evaluator returning success after cancellation cannot finalize that result.

The operation context and run deadline remain observable through Stop, collector
shutdown, and runtime Close. Their final synchronous observation, together with
the fault latch, is Run's success boundary. Cancellation after this boundary is
outside the completed operation. Stop receives a detached context with the
configured stop budget so cancellation does not skip cleanup.

Collectors stay active through Stop and drain available evidence before shutdown.
Canceled and failed runs retain known worker and unstarted results in scenario
order. A committed result keeps its nil worker error even when Run returns the
operation context error. Evaluation and run fingerprint are provisional and are
cleared on any run error. If a trace observer rejects an event, trace recording
stays stopped at that event while cleanup still collects worker facts.

## Errors and verdicts

Worker errors remain evidence for the oracles. Unstarted, protocol, execution,
evaluation, session, operation-context, and Stop errors fail Run; independent
causes are joined rather than masked. Collection errors are ordered by scenario
worker order. `errors.Is` and `errors.As` retain the underlying causes and typed
session/unstarted errors. Internal cancellation used to wake runtime waiters
reports its actual cause instead of introducing an unrelated operation-canceled
error. Only oracles judge invariants; the coordinator does not create violations.

No new diagnostic, CLI configuration key, or report artifact version is introduced.
Fixture quarantine after uncertain cleanup remains
[#120](https://github.com/weavegate/weavegate/issues/120); this boundary does not
claim that a failed Stop makes the fixture safe to reset.

## Reproducing the evidence

The focused suite uses channel barriers rather than sleeps for ordering. Run it
without Docker:

```bash
go test ./internal/orchestrator ./internal/sut ./internal/sut/gonative \
  -run 'TestOutcome|TestFaultLatch|TestGoNativeUnstartedWorkerCleanup|TestGoNativeAsyncUnstarted|TestGoNativeCleanupSessionFault|TestWorkerAcceptance' \
  -v -count=20
go test -race ./internal/orchestrator ./internal/sut ./internal/sut/gonative \
  -run 'TestOutcome|TestFaultLatch|TestGoNativeUnstartedWorkerCleanup|TestGoNativeAsyncUnstarted|TestGoNativeCleanupSessionFault|TestWorkerAcceptance' \
  -count=20
```

The actual MySQL transaction test commits an update, signals the commit barrier,
then observes cancellation before returning the command's nil error. It verifies
the committed row through the fixture pool and checks connection return:

```bash
go test ./internal/sut/gonative \
  -run 'TestGoNativeMySQL/preserves_commit_before_cancellation' -v -count=20
```

These fixed test markers are checked with `grep -F` in the smoke workflow:

- `SUT_FAULT_LATCH_RESULT`
- `SUT_SESSION_FAULT_RESULT`
- `SUT_CANCEL_BOUNDARY_RESULT`
- `SUT_UNSTARTED_RESULT`
- `SUT_OUTCOME_ERRORS_RESULT`
- `SUT_OUTCOME_STREAM_RESULT`
- `SUT_ASYNC_UNSTARTED_RESULT`
- `SUT_COMMAND_ACCEPT_RESULT`
- `SUT_COMMIT_CANCEL_RESULT`

The markers describe Go boundary and MySQL adapter evidence. They do not claim
execution of the shared external wire vectors or Java lifecycle conformance.
