# Matching-slice replay determinism

This experiment records repeat evidence for the matching-slice fixture on
MySQL 8.4. It replays one saved schedule against both application variants and
compares a normalized run fingerprint after every reset and run. The
fingerprint combines the complete Oracle evaluation, normalized worker
terminals, and ordered control trace.

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

| Variant | Runs | Active-assignment Oracle | Workflow-count Oracle | Blocked runs |
| --- | ---: | --- | --- | ---: |
| Vulnerable | 20/20 | Violation with request `42`, count `2` | PASS with exact `2/2/2` counts | 0/20 |
| Fixed | 20/20 | Evaluated PASS | PASS with exact `1/1/1` counts | 20/20 |

Each variant produced exactly one normalized run fingerprint across its 20
runs, so the reported replay result is `flaky=false`. Both PASS results and
violations remain in the Oracle evaluation that is fingerprinted. The exact
workflow-count Oracle is evaluated before every reset, so a count deviation in
any run becomes a violation rather than being hidden by the final snapshot.
The fixed replay also observed an InnoDB row-lock wait in 20/20 runs. Both
variants completed with zero worker errors and zero observed MySQL deadlock
terminals.

The fingerprint excludes generated row IDs, raw worker errors, durations, and
wall-clock values. Stable database evidence enters it through the normalized
Oracle results rather than a separate state-fingerprint string.

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

The trace used here is an ordered, in-memory draft of control events. This
fixture-test path does not write run artifacts; the CLI artifact's public
[`trace.json`](../reference/report-schema.md#tracejson) schema is documented
separately. The MySQL `1213` category is covered by unit-level
failure-classifier evidence, but this fixture did not observe an actual
deadlock. The reusable SQL assertions now provide the invariant and exact-count
verdicts for this saved schedule. Clean-run differential evidence and
schema-constraint evidence remain pending.
