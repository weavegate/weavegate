# ADR 0001: Worker-owned database connections

- Status: Accepted
- Date: 2026-08-12

## Context

The Go-native SUT adapter runs application commands concurrently against a real
database. A shared `*sql.DB` chooses a pooled connection for each operation, so
the adapter cannot tell which database session owns a transaction or lock. That
ambiguity breaks the relationship between a weavegate worker, its command, and
the database state observed while that command is running.

The fixture database handle also has lifecycle responsibilities: it provisions
the schema, restores the seed, and exposes committed state for assertions. Using
that handle directly for worker operations would mix fixture observation with
SUT execution.

## Decision

Each invocation acquires one dedicated `*sql.Conn` before starting its worker
goroutine. The adapter owns that connection for the complete command lifecycle
and closes it after the command returns. A worker ID therefore identifies one
command execution on one database session.

Worker IDs are unique among active invocations. An ID remains reserved until
its connection has been returned and its terminal result stream and cleanup
signal have both been finalized; only then may a later invocation reuse it.

The fixture handle remains the observation surface. It is used to prepare and
reset the database and to query committed results, but it does not execute a
worker's transactional workflow.

Future out-of-process adapters must preserve the same ownership rule: one
worker maps to one database session for the duration of a command, even when
the mechanism used to establish that session differs.

## Consequences

- Concurrent execution needs at least as many available connections as active
  workers.
- Transaction and lock observations can be attributed to the worker that owns
  the corresponding session.
- Duplicate active worker IDs are rejected before another connection is
  acquired, while terminally completed IDs can be reused.
- Cancellation and adapter shutdown must wait for command completion and
  connection release.
- Fixture reset must not run while workers are active.

## Alternatives considered

The alternative was to let commands issue operations through the shared pool.
That requires less lifecycle code, but consecutive operations are not visibly
tied to a worker-owned session. It was rejected because controlled transaction
and lock behavior requires explicit session ownership.
