# Frequently Asked Questions

## Why not just use Testcontainers?

**Objection:** An integration test already starts a real database with
Testcontainers, so another tool is unnecessary.

**Fact:** Testcontainers supplies the disposable MySQL environment; it does not
choose or preserve the order in which application workers cross a contested
decision. An ordinary concurrent launch can detect the same defect, but its
observed ordering is not saved as a replayable schedule.

**Evidence:** The
[baseline comparison](experiments/baseline-comparison.md#measured-result) runs
the same fixture and SQL oracle through plain, delayed, serial, and
schedule-controlled arms. Its
[evidence boundary](experiments/baseline-comparison.md#evidence-boundary) states
exactly which reproducibility result the measurement supports. Weavegate still
uses Testcontainers for the real MySQL instance.

## Why not use Lincheck?

**Objection:** Lincheck already explores and replays concurrent executions.

**Fact:** Lincheck checks concurrent JVM data structures against
linearizability. Weavegate controls named boundaries in an application workflow
that commits through a real database, then evaluates domain invariants with SQL
oracles. Those are different systems under test and different verdicts.

**Evidence:** The [Lincheck comparison](related-work.md#lincheck) records both
the borrowed exploration/replay idea and the boundary. The current fixture's
[SQL assertion verdict](../fixtures/matching-slice/README.md#sql-assertion-verdict)
shows the database rows used for weavegate's judgment.

## Why not use a database isolation tester?

**Objection:** PostgreSQL's isolation tester and Hermitage already enumerate
transaction schedules.

**Fact:** Those tools characterize database-engine behavior. Weavegate accepts
the database's permitted behavior as an input and asks whether an application
workflow preserves its own invariant, including whether the implemented fix
passes the same controlled schedule.

**Evidence:** The
[side-by-side boundary](related-work.md#the-question-side-by-side) separates
the layer each tool tests. The
[fix evidence](why-the-fix-works.md#locking-mechanism) follows the application
path from an unlocked read to `FOR UPDATE` and records the oracle result.

## Why must I add sync-points manually?

**Objection:** Manual instrumentation requires hot-spot knowledge and could
miss another interleaving.

**Fact:** That limitation is real. Sync-points deliberately name a small set of
semantic `read -> decide -> write` boundaries; weavegate does not claim to find
every race automatically. In return, a saved schedule refers to stable domain
boundaries rather than source line numbers or timing guesses, and production
construction uses a no-op implementation.

**Evidence:** The [instrumentation guide](howto/instrument.md) shows the two
reference points and no-op seam. The
[schedule limitation](limitations.md#a-saved-schedule-is-coordination-intent)
bounds what their control proves, and the reference fixture's
[`service.go`](../fixtures/matching-slice/sut/service.go) is the executable
implementation.
