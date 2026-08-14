# ADR 0003: Oracle evaluation boundary

- Status: Accepted
- Date: 2026-08-15

## Context

A replay verdict must describe committed database state after every scheduled
worker has reached a terminal outcome. Evaluating earlier can observe an open
transaction, consume a connection still owned by a worker, or read state that
will later be rolled back. Evaluating after fixture reset would instead inspect
the seed rather than the run.

The verdict also needs to distinguish three outcomes. A domain invariant may
pass, it may return stable evidence for a violation, or evaluation may fail
because a query, scan, rollback, or deadline failed. Treating an omitted result
as a pass or treating an infrastructure error as a violation would make replay
fingerprints ambiguous.

## Decision

A worker terminal is published only after its command transaction has committed
or rolled back and its worker-owned connection has been returned to the pool.
The orchestrator collects every terminal, records `schedule_complete`, and then
evaluates the configured Oracle set exactly once. Adapter stop and runtime close
happen after evaluation as bounded cleanup. A replay starts its next fixture
reset only after the preceding evaluation has finished.

Oracle evaluation shares the run's existing `RunTimeout`. Fixture reset,
scheduled execution, terminal collection, and evaluation all consume that one
budget; there is no separate evaluation timeout. Cleanup still runs if the
evaluator returns an error or the shared deadline expires.

The evaluator receives cloned normalized terminals and trace events. Raw worker
errors, durations, and wall-clock values are outside the Oracle input and the
replay fingerprint. The database interface exposed to an Oracle contains only
transaction creation. The orchestrator supplies the managed fixture database,
not a worker-owned connection or an arbitrary operational database.

SQL assertions start a read-only transaction and always roll it back. Assertion
queries are expected to be read-only. A successful query that returns zero rows
is a pass; returned rows become an assertion violation with canonical evidence.
Query, scan, close, rollback, and deadline failures are evaluation errors and do
not become violations or flaky replay results.

Every configured Oracle produces one result, including a passing Oracle with an
empty violations array. An empty result set is invalid. The evaluation
fingerprint includes Oracle IDs and these explicit pass results. The run
fingerprint combines the evaluation fingerprint, normalized terminals, and
normalized trace in a fixed-key JSON payload before hashing.

## Consequences

- Oracle queries observe the post-terminal committed state while worker-owned
  connections are already available to the pool.
- A passing result proves that a configured Oracle was evaluated; absence of a
  result cannot silently mean pass.
- Evaluation failures remain run errors, while stable domain violations remain
  data that repeated replay can compare.
- Long-running or locking assertion queries can exhaust the remaining run
  budget. Assertion authors must avoid writes and locking reads.
- The current boundary does not capture a pre-worker baseline or construct a
  golden snapshot. Those capabilities require a separate design.
