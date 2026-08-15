# ADR 0004: Schedule exploration boundary

- Status: Accepted
- Date: 2026-08-15

## Context

Replay can reproduce a saved coordination schedule, but it cannot discover one.
Exploration needs a deterministic candidate order, a bounded way to reject an
impractical candidate space, and an artifact contract that turns the first
observed violation into an ordinary replay input.

A saved coordination schedule is a total order of worker-and-point intents. It
contains every declared point for every worker while preserving each worker's
point order. It is not the same thing as the release order realized by a run.
A worker can be blocked in the database, can arrive after a timeout inference,
or can finish before a later point. Those events can cause the orchestrator to
release a pending point later or mark a remaining intent as terminal-skipped.

The distinction matters for coverage claims. Enumerating saved intents gives a
precise, reviewable candidate space for one scenario. It does not establish
complete coverage of database-level concurrency behavior or every release
order that another engine might realize.

## Decision

Schedule enumeration is exposed through a strategy interface. A plan reports
whether its candidate total is known and supplies candidates in strategy order.
This lets the orchestrator consume exhaustive and future non-exhaustive
strategies without depending on a particular implementation.

The exhaustive strategy preserves each worker's declared point order and uses
lexicographic worker-declaration order. At each position, it tries the first
worker with a remaining point before trying later workers. The result is a
canonical, deterministic depth-first traversal. It counts candidates before
yielding any of them and rejects a count above the configured maximum. The
default maximum is 5,000 candidates.

Every candidate is content-addressed from its canonical step list. The content
hash is the replay key, so identical coordination intent has the same schedule
ID regardless of where it was discovered. The schedule writer is the inverse
of the existing loader: it verifies the content ID and writes the loader's
canonical indented JSON form with one trailing newline. File writes use a
same-directory temporary file followed by replacement.

The orchestrator runs candidates in strategy order and stops at the first
violating Oracle evaluation. It reports both the candidate total, when known,
and the number actually evaluated. The discovered schedule and its complete run
evidence are returned for persistence and replay. Exploration performs no
minimization, so "first" means first in strategy order rather than smallest or
simplest.

A run failure or Oracle failure is an exploration error, not a violation. An
evaluation with no Oracle results is also an error: absence of a verdict cannot
support a clean exhaustive result. Partial candidate summaries remain available
for diagnostics when an error interrupts traversal.

Saved intent and realized execution remain separate evidence. The saved file
records which worker-and-point arrivals the coordinator intends to release in
order. The normalized trace records what the engine actually observed and
released under database locks, timeouts, pending-point draining, and terminal
workers. The schedule ID addresses only the saved intent; the run fingerprint
also includes the realized trace, terminals, and Oracle evaluation.

## Consequences

- A discovered violation becomes a canonical artifact that the ordinary loader
  and replay path can consume without a second schedule format.
- Exhaustive exploration is deterministic for a fixed scenario declaration,
  while the strategy seam can later support plans whose totals are unknown.
- Candidate counts grow combinatorially. The pre-run maximum prevents a large
  exhaustive plan from starting fixture work, but it also limits the scenarios
  this strategy can handle.
- Stopping at the first violation saves work and produces one reproducible
  asset, but it does not find every violating candidate and does not minimize
  the discovered schedule. A separate census or minimizer is required for
  those questions.
- The explored space is the valid saved coordination schedules for the given
  workers and points. Database locks can make several saved intents converge on
  similar realized behavior or produce a realized release order different from
  the file.
- Run errors and missing verdicts cannot be presented as either a discovered
  violation or a clean sweep.
