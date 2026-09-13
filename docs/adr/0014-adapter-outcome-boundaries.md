# ADR 0014: Adapter faults, unstarted invocations, and run cancellation

- Status: Accepted
- Date: 2026-09-13
- Issue: [#119](https://github.com/weavegate/weavegate/issues/119)

## Context

ADR 0010 identified three independent result boundaries needed before external
execution can be enabled. A worker result requires a completed transaction and
returned connection. Session failure and operation cancellation cannot truthfully
be encoded as that worker result. This decision changes the Go adapter boundary;
it does not implement the external transport or fixture quarantine.

## G2: Latched session faults

Every started Handle exposes a read-only session fault surface. Its notification
channel closes once, and its typed error retains the first failure and its cause
for every observer, including observers arriving after notification. The adapter
must latch a fault before closing an invocation stream whose outcome is unknown.
Unknown transaction outcome or unproven connection cleanup never produces a
WorkerResult. Successful Stop must finish publishing session faults before returning; a
failed Stop provides only an observed fault snapshot and does not prove cleanup.
Quarantine after failed Stop remains the separate G3 decision.

Run observes faults during execution and provisional oracle evaluation by
canceling their shared execution context. It also reads the latch synchronously
before evaluation and after Stop and collector cleanup. An evaluator must honor
its context; even an evaluator that returns success after cancellation cannot
make that provisional success final. Fault notification remains independent of
invocation streams, including after all workers have completed.

## G5: One invocation stream with distinct outcomes

Invoke returns one stream of InvocationOutcome, containing exactly one of a
WorkerResult or an UnstartedResult, followed by closure. A synchronous rejection
returns an error and no stream. An asynchronous UnstartedResult carries the worker
identity and cause and proves that no command transaction began and all acquired
resources were returned. Failed or unknown cleanup is a session fault instead.
Neither an unstarted outcome nor a stream closure calls runtime Finish or creates
a rollback, worker terminal, or oracle verdict. The coordinator aborts execution,
collects the outcome, and returns its cause as a run error.

Adapters release worker reservations atomically with stream closure, after
cleanup, permitting identity reuse only after closure. Collectors verify identity,
exclusive outcome shape, a non-nil unstarted cause, and exactly one outcome plus
closure. Empty, multiple, or unfinished streams are protocol errors. A truthful
WorkerResult authorizes Finish immediately; oracle evaluation additionally waits
for stream closure. Shutdown keeps collectors alive through Stop so unbuffered
producers can finish, then drains available evidence within the cleanup boundary.

## G6: Cancellation and finalization

The operation context and configured run deadline remain observable through
execution, collection, evaluation, Stop, collector shutdown, and runtime Close.
The final synchronous context/latch observation after those cleanup operations
is the success boundary. Cancellation after that observation belongs to the
caller, not this completed Run. Internal execution cancellation for faults or
unstarted outcomes is separate from operation cancellation and does not invent
a context.Canceled error for the caller.

Committed worker evidence retains its nil worker error even when cancellation
wins the operation. Collected worker and unstarted evidence is retained in
scenario order on unsuccessful runs, including outcomes published during Stop.
Provisional evaluation and run fingerprint are cleared whenever Run returns an
error; only oracles decide invariant results.

## Precedence and aggregation

| Surface | Treatment |
| --- | --- |
| Worker error | Preserved in WorkerResult and terminal evidence; alone does not abort Run or replace oracle judgment. |
| Unstarted/protocol/execution/evaluation error | Run error; preserve collected evidence. |
| Session fault | Typed Run error with original cause, even if evaluation returned success. |
| Operation cancellation/deadline | Join the operation context error; never rewrite a worker result. |
| Stop failure | Join the cleanup error; cannot mask any preceding cause. |

Run errors dominate provisional verdicts. Independent errors are joined in
execution, scenario-ordered collection, Stop, session-fault, operation-context
order; already represented causes need not be duplicated. errors.Is/errors.As
remain usable for all retained causes. No total priority discards another error.
Worker errors remain evidence when any of these errors coexist.

## Validation scope

Barrier-driven Go tests cover faults before, during, and after evaluation,
commit-before-cancel, cancellation before acceptance, stream closure, and combined
errors. Exact commands and fixed CI markers are recorded in the
[adapter outcome reference](../reference/adapter-outcomes.md). These Go boundary
tests do not claim Java transaction or external transport conformance.
