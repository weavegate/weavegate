# Related work & attribution

replaygate did not appear from nowhere. It borrows ideas from a lineage of
concurrency- and database-testing tools, and it depends on real infrastructure
to run. This document credits that prior art and — just as importantly — draws
the line between what those tools do and what replaygate does.

The short version: most of the tools below ask **"does the database permit this
anomaly?"** replaygate asks a different question — **"does *your application
code* survive the anomalies the database legitimately permits, and does your fix
close them?"** replaygate applies none of them as-is.

## The question, side by side

| Tool | Question it answers | Layer under test |
|---|---|---|
| Hermitage | Which anomalies does isolation level *X* on DB *Y* permit? | DB engine / isolation level |
| Jepsen | Does a distributed system uphold its claimed consistency under faults? | DB / distributed system |
| PostgreSQL isolation tester | Does Postgres produce the specified result for a given permutation? | DB engine |
| Lincheck | Is this concurrent data structure linearizable across thread interleavings? | In-JVM object |
| Deterministic-simulation testing (FoundationDB, TigerBeetle) | Does the whole system hold under a deterministically replayable world? | Entire system |
| **replaygate** | **Does your `read → decide → write` workflow hold under the schedules the DB permits — and does your fix close them?** | **Your application code** |

## Prior art we draw ideas from

### Hermitage
[Hermitage](https://github.com/ept/hermitage) is a suite of hand-crafted
transaction schedules that probe exactly which isolation anomalies each database
and isolation level actually permits (as opposed to what the standard claims).

- **What we borrow:** the discipline of expressing an anomaly as a concrete,
  minimal, ordered sequence of statements across sessions — and the insight that
  isolation levels are best understood by the schedules they *allow*, not by
  their names.
- **How replaygate differs:** Hermitage tests the *database*. replaygate takes
  the anomalies the database permits as a given and tests whether *your
  workflow* survives them.

### Jepsen
[Jepsen](https://jepsen.io/) analyzes real databases and distributed systems for
consistency violations under partitions and faults; its MySQL analysis is part
of why replaygate treats "MySQL permits this schedule" as a documented fact
rather than a bug to file.

- **What we borrow:** the stance that a system keeping every promise it *made*
  can still permit behavior that breaks *your* invariants — and that the honest
  move is to reproduce it deterministically, not to argue about it.
- **How replaygate differs:** Jepsen stress-tests the datastore itself under
  faults. replaygate assumes a healthy, single-node MySQL/InnoDB and probes the
  application logic layered on top.

### PostgreSQL isolation tester (`isolationtester` / `pg_isolation_regress`)
Postgres ships a
[test harness](https://github.com/postgres/postgres/tree/master/src/test/isolation)
that runs multi-session permutations of SQL steps and diffs the output against an
expected result, letting it assert precise interleaving behavior.

- **What we borrow:** the core mechanic — named steps, controlled permutations,
  and a blocking/waiting model so sessions release in a *chosen* order rather
  than a racy one. replaygate's sync-points are this idea moved out of the DB's
  own test suite and into your application's hot spots.
- **How replaygate differs:** the isolation tester validates PostgreSQL's own
  behavior against golden output. replaygate has no golden output for your
  workflow — it derives correctness from your SQL oracles, a clean-run
  differential, and schema constraints.

### Lincheck
[Lincheck](https://github.com/JetBrains/lincheck) verifies concurrent JVM data
structures by generating thread interleavings and checking linearizability,
including a mode that deterministically replays a failing interleaving.

- **What we borrow:** bounded exploration of interleavings plus deterministic
  replay of the specific one that failed, reported back to the developer as a
  reproducible artifact rather than a flaky failure.
- **How replaygate differs:** Lincheck reasons about in-memory objects and JMM
  visibility. replaygate's "shared state" is committed rows in a real
  transactional database, and its correctness criterion is your domain
  invariants, not linearizability of a data structure.

### Deterministic-simulation testing — FoundationDB & TigerBeetle
[FoundationDB](https://apple.github.io/foundationdb/testing.html) pioneered, and
[TigerBeetle](https://tigerbeetle.com/) popularized, building the system so its
entire execution — scheduling, I/O, faults, clocks — is driven by a single seed
and can be replayed bit-for-bit.

- **What we borrow:** the replay contract. A failure is only useful if anyone can
  reproduce it from an artifact: **same schema, same seed, same schedule, same
  result.** That is why a violating schedule is a saved, re-runnable file, not a
  log line.
- **How replaygate differs:** those systems are *built* for full-world
  simulation from day one. replaygate retrofits deterministic replay onto an
  existing Spring Boot + MySQL workflow at a handful of sync-points — narrower in
  scope, but adoptable without rewriting your application.

## Infrastructure we run on

### Testcontainers
[Testcontainers](https://testcontainers.com/) gives every run a real, disposable
MySQL 8 / InnoDB instance in a container. replaygate deliberately tests against a
real engine, not a mock or an in-memory substitute — the whole point is the
behavior the *actual* database permits.

### MySQL / InnoDB documentation
MySQL's own
[locking reads](https://dev.mysql.com/doc/refman/8.0/en/innodb-locking-reads.html)
and
[transaction isolation](https://dev.mysql.com/doc/refman/8.0/en/innodb-transaction-isolation-levels.html)
documentation define the schedules InnoDB permits at each isolation level.
replaygate treats these as the specification of "legitimate" DB behavior — the
baseline it holds your application accountable against.

## Summary of the boundary

Every tool above is excellent at its own layer, and replaygate is not a
replacement for any of them:

- It is **not** a database engine tester — that is Hermitage / isolation-tester
  territory.
- It is **not** a distributed-systems fault injector — that is Jepsen.
- It is **not** a general concurrent-object checker — that is Lincheck.
- It is **not** a from-scratch simulation runtime — that is FoundationDB /
  TigerBeetle.

replaygate occupies the gap they leave: the application code that sits on top of
a correct database and can still corrupt state under a schedule the database was
always allowed to produce.

## Corrections & additions

If replaygate mischaracterizes any project above, or if there is prior art we
should credit and don't, please open an issue — accurate attribution matters to
us.
