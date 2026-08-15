# Matching-slice schedule exploration

This experiment records how exhaustive saved-schedule exploration discovers
and reproduces the matching-slice invariant violation on MySQL 8.4. The
measurement was taken on 2026-08-15 with the repository's Docker fixture.

## Assumptions

- The fixture starts with active request `42` and no matching sessions or
  assignments.
- Workers `w1` and `w2` run the same `assign` command on separate connections.
- Each worker declares `after_read_request` followed by
  `before_insert_assignment`.
- A candidate is valid when it contains every worker-and-point pair once and
  preserves that order within each worker.
- Exploration evaluates only the `active-assignment-is-unique` invariant. The
  variant-specific workflow-count Oracle used by the older replay experiment
  is not part of the discovery verdict.
- The run, step, lock-inference, and stop deadlines are the same as the saved
  replay test.

These assumptions produce six valid saved coordination schedules. In canonical
worker order they are:

| Candidate | Worker sequence |
| ---: | --- |
| 1 | `w1 w1 w2 w2` |
| 2 | `w1 w2 w1 w2` |
| 3 | `w1 w2 w2 w1` |
| 4 | `w2 w1 w1 w2` |
| 5 | `w2 w1 w2 w1` |
| 6 | `w2 w2 w1 w1` |

Candidate 2 is the previously committed `sch_ba00582f9632` replay fixture.
Exploration discovered candidate 1, so the existing file remains an independent
replay regression asset rather than being replaced.

## Method

1. Build the exhaustive plan and run the vulnerable variant until its first
   invariant violation. Repeat that exploration 20 times.
2. Save the discovered schedule through the canonical writer, reload it through
   the ordinary loader, and replay it 20 times.
3. Run all six vulnerable candidates without early stopping. Repeat this census
   three times and compare the violating candidate indexes.
4. Explore the same six candidates on the `FOR UPDATE` variant five times,
   requiring complete exhaustion and no invariant violation in every repeat.
5. Count normalized worker failures, deadlocks, pending resolutions, and
   terminal-skipped runs from the execution evidence.

The save/reload check uses a temporary directory. It does not add or replace a
schedule under `fixtures/`.

## Measured result

| Measurement | Result |
| --- | --- |
| Vulnerable exploration | 20 repeats; candidate 1 found in every repeat |
| Discovered schedule | `sch_7dcb74b1e506`; one distinct ID |
| Discovery fingerprints | One distinct normalized run fingerprint |
| Candidates actually evaluated | 20 total; one per exploration repeat |
| Saved schedule replay | 20/20 invariant violations; one fingerprint |
| Fingerprint equality | Replay fingerprint equals the discovery fingerprint |
| Vulnerable census | 6/6 candidates violating in each of 3 repeats; 18 runs |
| Fixed exploration | 6 candidates x 5 repeats; 30/30 evaluated PASS |
| Fixed realized control evidence | 30 pending-resolved runs; 30 terminal-skipped runs |
| Terminal failures | 0 worker errors; 0 MySQL deadlocks |

The discovered schedule was serialized as:

```json
{
  "id": "sch_7dcb74b1e506",
  "steps": [
    {
      "worker": "w1",
      "point": "after_read_request"
    },
    {
      "worker": "w1",
      "point": "before_insert_assignment"
    },
    {
      "worker": "w2",
      "point": "after_read_request"
    },
    {
      "worker": "w2",
      "point": "before_insert_assignment"
    }
  ]
}
```

The vulnerable census confirms that the first result is not an isolated
candidate: all six candidates produced the invariant violation in each census
repeat. Early-stopping exploration nevertheless reports candidate 1 because
that is the first candidate in canonical strategy order.

On the fixed variant, database locking controls when `w2` can reach its first
sync-point. All 30 runs contained a timeout-inferred pending point that was
later released, and a later intent for the completed worker was
terminal-skipped. This is why the saved coordination intent must not be read as
the release order actually realized by the engine.

## Reproduction

Run the experiment with Docker available:

```bash
go test ./fixtures/matching-slice/sut \
  -run '^TestExploreConcurrentAssign$' -v -count=1
```

The test emits these stable result shapes; the schedule ID and candidate index
are observed values rather than hard-coded expectations:

```text
MATCHING_EXPLORE_RESULT variant=vulnerable candidates=6 repeats=20 evaluated=20 distinct_schedules=1 distinct_indices=1 distinct_fingerprints=1 violating_index=1 schedule=sch_7dcb74b1e506 saved=reloaded replay_repeat=20 violation_runs=20 fingerprint_match=true worker_errors=0 deadlocks=0 flaky=false
MATCHING_EXPLORE_CENSUS variant=vulnerable candidates=6 repeats=3 evaluated=18 violating_candidates=6 stable=true
MATCHING_EXPLORE_RESULT variant=fixed candidates=6 repeats=5 evaluated=30 violating=none exhausted=true pending_resolved_runs=30 terminal_skipped_runs=30 worker_errors=0 deadlocks=0
```

## Evidence boundary

This experiment exhausts the six valid saved coordination schedules of this
two-worker, two-point scenario. It does not prove complete realized-interleaving
coverage, applicability to another schema or database version, or practical
exhaustive search for larger scenarios. The candidate count grows rapidly and
the exhaustive strategy rejects plans above its configured bound.

The discovered candidate is not minimized. The normalized trace and run
fingerprint capture the execution realized under current database locks, but
the saved schedule remains coordination intent. Non-exhaustive strategies,
trace-based realized-order coverage, schedule minimization, CLI wiring, and
public report artifacts remain future work. The design boundary is recorded in
[ADR 0004](../adr/0004-schedule-exploration-boundary.md); the older committed
schedule's replay evidence remains in
[Matching-slice replay determinism](determinism.md).
