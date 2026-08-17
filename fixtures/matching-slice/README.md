# Matching-slice fixture

The matching-slice fixture is a synthetic database model for testing duplicate
assignment scenarios. It does not contain production data or reproduce a
service's private schema.

The domain invariant is:

> One active `project_request` has at most one active assignment to a
> `matching_session`.

## Data model

The fixture contains three InnoDB tables:

- `project_request` represents work waiting to be matched.
- `matching_session` represents a destination for matched work.
- `assignment` links a request to a session and records its status.

`assignment.project_request_id` and `assignment.matching_session_id` have
foreign keys to their parent rows. The request column is indexed but is
intentionally not unique, so a later concurrency test can represent two active
assignments for the same request.

The seed contains one active request:

| Table | Seed state |
| --- | --- |
| `project_request` | `id=42`, `status=ACTIVE` |
| `matching_session` | Empty |
| `assignment` | Empty |

## Reset contract

Each test run can mutate the fixture and then restore the same seed state
without restarting the MySQL container. The integration test verifies the
following transition:

```text
seed:    project_requests=1 matching_sessions=0 assignments=0
mutate:  project_requests=1 matching_sessions=1 assignments=1
reset:   project_requests=1 matching_sessions=0 assignments=0
```

Run the fixture test with Docker available:

```bash
go test ./fixtures/matching-slice/... -v -count=1
```

A successful run emits this stable marker:

```text
MATCHING_FIXTURE_RESULT request_id=42 project_requests=1 matching_sessions=0 assignments=0 reset=true
```

## Assignment workflow

The fixture includes a Go-native assignment SUT under `sut/`. Its command path
is split into three layers:

1. The handler accepts the adapter command and worker-owned connection.
2. The service owns the transaction and the read-check-insert workflow.
3. The repository executes every query through the service-owned `*sql.Tx`.

The service reads an active request, checks for an existing active assignment,
creates a matching session, and inserts the assignment. A second assignment for
the same request is a successful no-op after the existing assignment is found.

The variants differ only in the request read:

```sql
-- vulnerable
SELECT status FROM project_request WHERE id = ? AND status = 'ACTIVE';

-- fixed
SELECT status FROM project_request WHERE id = ? AND status = 'ACTIVE' FOR UPDATE;
```

The workflow exposes two named coordination points:

- `after_read_request`
- `before_insert_assignment`

The default coordination implementation is a no-op. The replay integration
test instead gives the reusable in-process sync-point runtime to the
orchestrator and registers workers `w1` and `w2`. The runtime pauses `Arrive`
calls until the orchestrator performs a targeted release for that exact worker
and point.

The saved schedule is
[`concurrent-assign.json`](schedules/concurrent-assign.json), with ID
`sch_ba00582f9632`. Its four steps target `w1` and `w2` at
`after_read_request`, followed by the same workers at
`before_insert_assignment`.

In the vulnerable replay, both workers pass the plain request read, see no
existing assignment, and arrive at `before_insert_assignment`. Releasing and
finishing `w1` before releasing `w2` produces two active assignments.

In the fixed replay, `w1` holds its locking request read while `w2` starts.
When `w2` does not reach `after_read_request` within 250 ms, the orchestrator
records the nonterminal, timeout-inferred `db_blocked` state. This timeout is
not proof of a database lock. The integration test separately verifies that
MySQL reports at least one current InnoDB row-lock wait before it releases
`w1`. After `w1` commits, `w2` reaches and is released from
`after_read_request`, sees the committed assignment, and finishes without
visiting `before_insert_assignment`.

Normal point coordination uses a 5-second step deadline and each complete
scenario has a 15-second deadline. These deadlines fail the test; only explicit
targeted release advances a paused worker.

## SQL assertion verdict

Both variants use the same two SQL assertion Oracles. The first is named
`active-assignment-is-unique`:

```sql
SELECT
    project_request_id,
    COUNT(*) AS active_assignment_count
FROM assignment
WHERE status = 'ACTIVE'
GROUP BY project_request_id
HAVING COUNT(*) > 1
ORDER BY project_request_id;
```

This is a zero-row assertion. Zero returned rows produce an explicit evaluated
PASS result for the configured Oracle. Returned rows produce an assertion
violation and become canonical evidence. The vulnerable variant produced one
violation in 20/20 runs with this evidence in every run:

```json
{"active_assignment_count":2,"project_request_id":42}
```

The second Oracle, `matching-workflow-counts`, returns the three actual
workflow counts only when they differ from the variant's expected state. It
requires sessions/assignments/active assignments of `2/2/2` for the vulnerable
variant and `1/1/1` for the fixed variant. This makes every run's complete count
state part of its Oracle evaluation and replay fingerprint.

The fixed variant produced two evaluated Oracle results with zero violations in
20/20 runs. The vulnerable variant's count Oracle also passed in 20/20 runs. A
missing or empty result set is not accepted as PASS.

The vulnerable CLI run names that assertion result without any diagnostic key
in the fixture configuration. Its saved `report.md` contains:

```text
## weavegate: FAIL (RG001)
scenario: concurrent-assign | schedules explored: 1 | violating: sch_7dcb74b1e506
assertion: active-assignment-is-unique
flaky: false (repeat=20)
replay: weavegate run --config fixtures/matching-slice/.weavegate/config.yaml --scenario concurrent-assign --variant vulnerable --replay sch_7dcb74b1e506 --repeat 20

error[RG001]: concurrent write not serialized
  observed:  active-assignment-is-unique returned 1 row: active_assignment_count=2 project_request_id=42
  assertion: active-assignment-is-unique
  invariant: a declared state invariant must hold under every release schedule the database permits
  reason:    read-then-write without a lock or a unique constraint allows interleaving
  help:      add a unique constraint on the active assignment key
             take a pessimistic lock (SELECT ... FOR UPDATE) before insert
             use an idempotency key on the write
  evidence:  schedule sch_7dcb74b1e506 · trace.json · 1 violating row
```

Evaluation starts after both worker terminals, so command transactions have
committed or rolled back and worker-owned connections have been returned. It
finishes before adapter cleanup and before the next fixture reset. The full
lifecycle decision is recorded in
[Oracle evaluation boundary](../../docs/adr/0003-oracle-evaluation-boundary.md).

Assertion queries must be read-only. Do not use writes or locking reads such as
`SELECT ... FOR UPDATE`; they can fail or consume the remaining run deadline.
MySQL read-only transaction enforcement is covered by a separate integration
test that attempts a write and verifies that the seed row remains unchanged.

Run the sequential SUT test, read-only boundary test, and saved schedule replay
with Docker available:

```bash
go test ./fixtures/matching-slice/sut -run TestAssignSequential -v -count=1
go test ./internal/oracle/sqlassert \
  -run '^TestMySQLReadOnlyAssertion$' -v -count=1
go test ./fixtures/matching-slice/sut \
  -run '^TestReplayConcurrentAssign$' -v -count=1
```

The replay repeats each application variant 20 times. It produces one stable
fingerprint per variant and emits these result markers:

```text
MATCHING_ORACLE_RESULT schedule=sch_ba00582f9632 variant=vulnerable oracle=active-assignment-is-unique count_oracle=matching-workflow-counts oracles_evaluated=2 repeat=20 violation_runs=20 violations=20 evidence_rows=20 evidence_json={"active_assignment_count":2,"project_request_id":42} flaky=false
MATCHING_ORACLE_RESULT schedule=sch_ba00582f9632 variant=fixed oracle=active-assignment-is-unique count_oracle=matching-workflow-counts oracles_evaluated=2 repeat=20 pass_runs=20 violations=0 evidence_rows=0 flaky=false
MATCHING_REPLAY_RESULT schedule=sch_ba00582f9632 variant=vulnerable repeat=20 duplicate_runs=20 blocked_runs=0 sessions=2 assignments=2 active_assignments=2 worker_errors=0 deadlocks=0 flaky=false
MATCHING_REPLAY_RESULT schedule=sch_ba00582f9632 variant=fixed repeat=20 pass_runs=20 blocked_runs=20 lock_wait_runs=20 sessions=1 assignments=1 active_assignments=1 worker_errors=0 deadlocks=0 flaky=false
SQL_ASSERT_MYSQL_READONLY_RESULT write=error state_unchanged=true
```

The legacy vulnerable marker's `duplicate_runs` field is a compatibility alias
for the Oracle marker's `violation_runs`; it is not an independent verdict. The
`2/2/2` and `1/1/1` row counts are evaluated inside every run. The final
snapshot check remains as a fixture regression check.

The full measurement, realized release behavior, and evidence limits are in
[Matching-slice replay determinism](../../docs/experiments/determinism.md).

## Schedule exploration

The scenario has two workers and two ordered sync-points. Preserving each
worker's point order yields six valid saved coordination schedules. The
exhaustive strategy counts that space before any fixture reset, enumerates it in
canonical worker-declaration order, and the orchestrator stops at the first
invariant violation.

Exploration uses only the `active-assignment-is-unique` assertion. The
variant-specific workflow-count assertion remains part of the existing saved
replay test, where it verifies the expected complete state for that particular
replay. Keeping it out of discovery prevents a variant-specific expected count
from defining which candidate is considered violating.

Across 20 vulnerable exploration repeats, candidate 1 was always the first
violation. Its content-addressed ID is `sch_7dcb74b1e506`. The test wrote that
schedule to a temporary artifact, loaded it through the ordinary schedule
loader, and replayed it 20 times. Every replay violated the invariant and the
single replay fingerprint equaled the fingerprint measured during discovery.

The discovered candidate differs from the committed
[`concurrent-assign.json`](schedules/concurrent-assign.json) regression fixture,
which is candidate 2. The committed file remains unchanged. A separate census
ran all six candidates three times without early stopping and observed the same
six violating candidate indexes in each repeat.

The `FOR UPDATE` variant exhausted the same six candidates five times. All 30
runs evaluated PASS with zero worker errors and zero deadlocks. Every run also
recorded a pending point that was later released and a terminal-skipped intent.
Those observations demonstrate that a saved coordination schedule records
release intent, while database locking can change the release order realized by
the engine.

Run the exploration with Docker available:

```bash
go test ./fixtures/matching-slice/sut \
  -run '^TestExploreConcurrentAssign$' -v -count=1
```

It emits these measured markers:

```text
MATCHING_EXPLORE_RESULT variant=vulnerable candidates=6 repeats=20 evaluated=20 distinct_schedules=1 distinct_indices=1 distinct_fingerprints=1 violating_index=1 schedule=sch_7dcb74b1e506 saved=reloaded replay_repeat=20 violation_runs=20 fingerprint_match=true worker_errors=0 deadlocks=0 flaky=false
MATCHING_EXPLORE_CENSUS variant=vulnerable candidates=6 repeats=3 evaluated=18 violating_candidates=6 stable=true
MATCHING_EXPLORE_RESULT variant=fixed candidates=6 repeats=5 evaluated=30 violating=none exhausted=true pending_resolved_runs=30 terminal_skipped_runs=30 worker_errors=0 deadlocks=0
```

The complete method, discovered schedule JSON, and evidence limits are in
[Matching-slice schedule exploration](../../docs/experiments/exploration.md).

## Current boundary

This fixture verifies the schema, seed and reset behavior, an application
assignment workflow, orchestrator-controlled saved replay, and exhaustive
execution of the six valid saved coordination schedules for this scenario.
Reusable SQL assertions evaluate the active-assignment invariant during
exploration and both that invariant and exact workflow counts during the older
replay. The lock-wait observation remains a separate assertion inside the
replay integration test.

This evidence applies to the declared two-worker, two-point scenario, fixture
state, MySQL image, and application variants. It does not establish complete
coverage of database-level concurrency behavior. Saved intent can differ from
realized release order under locks, candidate counts grow combinatorially, and
the discovered schedule is not minimized.

The evidence status is tracked in
[Why the fix works](../../docs/why-the-fix-works.md).
