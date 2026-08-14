# Matching-slice replay determinism

This experiment records repeat evidence for the matching-slice fixture on
MySQL 8.4. It replays one saved schedule against both application variants and
compares a normalized database-state fingerprint after every reset and run.

## Saved control schedule

The schedule artifact is
[`concurrent-assign.json`](../../fixtures/matching-slice/schedules/concurrent-assign.json).
Its content-addressed ID is `sch_ba00582f9632`, and its four control steps are:

1. release `w1` from `after_read_request`;
2. release `w2` from `after_read_request`;
3. release `w1` from `before_insert_assignment`; and
4. release `w2` from `before_insert_assignment`.

Both workers run the `assign` command for request `42`. The scenario declares
the `after_read_request` and `before_insert_assignment` points.

The saved list is the requested control order. The realized release order can
differ when a worker is blocked or has already completed:

- The vulnerable variant realizes all four releases in saved order. Both
  workers pass the plain read before either insert is released.
- The fixed variant releases `w1` after its locking read. The wait for `w2` at
  that point times out and is recorded as nonterminal `timeout-inferred`
  blocking. The orchestrator then releases `w1` before its insert. After `w1`
  commits, the pending `w2` read arrives and is released; `w2` observes the
  assignment and finishes, so its before-insert step is terminal-skipped.

At the fixed timeout event, the fixture separately checks that MySQL reports at
least one current InnoDB row-lock wait. The timeout inference itself is not a
lock detector.

## Repeated result

The fixture provisions one MySQL 8.4 container and resets the same schema and
seed before each replay. Each variant was repeated 20 times:

| Variant | Runs | State fingerprint | Blocked runs | Result |
| --- | ---: | --- | ---: | --- |
| Vulnerable | 20/20 | `sessions=2;assignments=2;active=2;duplicate=true` | 0/20 | Duplicate reproduced |
| Fixed | 20/20 | `sessions=1;assignments=1;active=1;duplicate=false` | 20/20 | Invariant preserved |

Each variant produced exactly one fingerprint across its 20 runs, so the
reported replay result is `flaky=false`. The fixed replay also observed an
InnoDB row-lock wait in 20/20 runs. Both variants completed with zero worker
errors and zero observed MySQL deadlock terminals.

The fingerprint intentionally contains stable row counts and the duplicate
predicate. It excludes generated row IDs.

## Timing and reproduction

The measured 40-run replay loop, excluding container provisioning, completed in
`8.781415552s`, with an average duration of `219.535388ms` per reset and run.
This was measured locally with MySQL 8.4 in Docker.

Run the same fixture replay with Docker available:

```bash
go test ./fixtures/matching-slice/sut \
  -run '^TestReplayConcurrentAssign$' -v -count=1
```

The test emits `MATCHING_REPLAY_RESULT` lines for the two variants and a
`MATCHING_REPLAY_TIMING` line for the combined 40 runs.

## Evidence boundary

This result establishes repeatability only for the recorded schedule with the
same fixture schema, seed, MySQL 8.4 image, and selected application variant.
It does not prove exhaustive exploration, behavior on another database or
version, or the absence of other concurrency defects. The
`timeout-inferred` event remains an orchestration observation; the separate
InnoDB status check supplies the lock-wait evidence for this fixture.

The trace used here is an ordered, in-memory draft of control events. It does
not define a public `trace.json` schema. The MySQL `1213` category is covered by
unit-level failure-classifier evidence, but this fixture did not observe an
actual deadlock. A reusable SQL Oracle and rule `RG001` remain pending.
