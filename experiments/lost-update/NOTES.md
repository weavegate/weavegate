# Lost update experiment

## Purpose

This experiment shows how an unlocked read-modify-write transaction can lose
updates when multiple clients modify the same MySQL/InnoDB row. It then runs the
same workload with `SELECT ... FOR UPDATE` to demonstrate that a locking read
preserves every update.

This is a runnable concurrency baseline, not a benchmark or a deterministic
replay test.

## Environment

- Go: `1.23.1`
- Database image: `mysql:8.4`
- Storage engine: InnoDB
- Isolation: the image default, InnoDB `REPEATABLE READ`; the test does not
  issue an explicit isolation-level statement
- Container runtime: Testcontainers for Go

## Workload

The test starts each path with one `account` row whose balance is `0`.

- Workers: 8 goroutines
- Increments per worker: 100
- Expected final balance: 800
- Connections: one dedicated `*sql.Conn` per worker
- Transaction boundary: one explicit transaction per increment
- Overlap aid: a 2 ms delay between the read and write

Each transaction reads the balance, waits 2 ms, writes the previously read
value plus one, and commits. The two paths differ only in the read query:

```sql
-- Vulnerable
SELECT balance FROM account WHERE id = 1;

-- Fixed
SELECT balance FROM account WHERE id = 1 FOR UPDATE;
```

## Observed results

A representative local run produced:

| Path | Expected | Observed | Lost | Workload elapsed |
| --- | ---: | ---: | ---: | ---: |
| Vulnerable | 800 | 100 | 700 | 786.773674 ms |
| Fixed | 800 | 800 | 0 | 5.541139593 s |

The elapsed value measures only the concurrent SQL workload. It starts after
the MySQL container is ready and the schema is applied, so it excludes
container startup and cleanup.

The complete test passed five consecutive runs in `171.516 s`, including the
startup and cleanup of ten independent MySQL containers. The exact vulnerable
lost count and elapsed time are environment-dependent; the required invariants
are a positive lost count for the vulnerable path and zero lost updates for the
fixed path.

## Why the outcomes differ

The vulnerable path uses a plain snapshot read. Concurrent transactions can
read the same balance and later write the same absolute value. Although MySQL
serializes the row updates themselves, a later transaction can still overwrite
an earlier result with a value computed from its stale read.

The fixed path acquires the row lock during the read. Other locking readers wait
until the current transaction writes and commits, then read the latest committed
balance. This serializes the complete read-modify-write operation and preserves
all 800 increments.

The fixed path takes longer because the workers intentionally wait for the same
row lock. That additional elapsed time is expected and is not treated as a
performance regression in this correctness experiment.

## Run the experiment

Go 1.23.1 is installed outside the default `PATH` in the current development
environment:

```bash
export PATH=/usr/lib/go-1.23/bin:$PATH
go test ./experiments/lost-update/... -v -count=1
```

The test emits stable result markers:

```text
LOSTUPDATE_RESULT path=vulnerable expected=800 observed=100 lost=700 elapsed=...
LOSTUPDATE_RESULT path=fixed expected=800 observed=800 lost=0 elapsed=...
```

Check repeatability with:

```bash
go test ./experiments/lost-update/... -count=5
```

## Limitations

The 2 ms delay only widens the opportunity for overlapping reads. It does not
control the exact transaction schedule, so the vulnerable path is a
probabilistic reproduction. Deterministic sync-points, replay, and CI execution
are follow-up work.
