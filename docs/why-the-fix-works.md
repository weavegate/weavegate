# Why the fix works

This page tracks the evidence that connects each weavegate fixture to a database
anomaly, an invariant check, and a verified fix. A row remains pending until the
repository contains an executable test and observable result for that stage.

## Coverage status

| Fixture | Anomaly | Fixture state | Controlled execution | Oracle | Fix evidence |
| --- | --- | --- | --- | --- | --- |
| [matching-slice](../fixtures/matching-slice/README.md) | Duplicate active assignment | Schema/seed ready | Saved schedule replay (20/20) | SQL assertion (20/20) | Locking replay 20/20 |

## Matching slice

The matching-slice fixture models one active project request that may be linked
to matching sessions through assignment rows. Its invariant is that one active
project request has at most one active assignment.

The vulnerable schema deliberately permits multiple assignment rows for the
same request. This makes a duplicate state representable, but schema capability
alone is not evidence that a concurrent workflow has produced the anomaly. The
Go-native assignment test executes that workflow through two worker-owned
database connections and a content-addressed saved schedule.

### Locking mechanism

Both variants run the same read-check-insert workflow inside a service-owned
transaction. The vulnerable variant reads the parent `project_request` without
a locking clause. Its two workers can both read the active request, observe no
active assignment, and reach `before_insert_assignment`. The in-process
sync-point runtime records both arrivals and the orchestrator performs a
targeted release for each worker. Each transaction then inserts a matching
session and an assignment.

The fixed variant reads the same parent row with `FOR UPDATE`. The first worker
holds that row lock while it checks and writes. The second worker's locking read
does not reach `after_read_request` before the runtime timeout, so the runtime
records a nonterminal, timeout-inferred `db_blocked` state. That inference is
not a lock detector. The integration test separately observes one current
InnoDB row-lock wait. Once the first transaction commits, the second locking
read continues, reaches `after_read_request`, and its subsequent check sees the
committed active assignment. The second command then returns the normal
already-assigned result without reaching `before_insert_assignment` or creating
another session or assignment.

### Verified today

- MySQL 8.4 creates the three fixture tables with the InnoDB engine.
- Assignment rows reference existing project requests and matching sessions.
- The project-request assignment index is not unique.
- Reset restores request `42` and removes all sessions and assignments.
- The reset behavior is exercised by the repository's smoke workflow.
- Each concurrent worker executes on its own database connection and preserves
  its worker ID through the coordination seam.
- The in-process sync-point runtime registers each worker, records named
  arrivals, performs targeted worker-and-point release, and receives terminal
  results from the test's result collector.
- Schedule `sch_ba00582f9632` drives four named worker-and-point control steps
  independently of the integration test implementation.
- The `active-assignment-is-unique` SQL assertion returned one violation in
  20/20 vulnerable runs. Every run contained the canonical request `42`, count
  `2` evidence row.
- The vulnerable replay also produced two sessions and two active assignments
  in 20/20 runs after both workers reached `before_insert_assignment`.
- The `FOR UPDATE` path paired the runtime's timeout inference with a separate
  InnoDB row-lock wait observation, then resumed the second worker.
- The locking replay produced one evaluated PASS result for the same SQL
  assertion in 20/20 runs, plus one session and one active assignment in every
  run. Both workers completed without a command error, and the second worker
  did not visit `before_insert_assignment`.
- A MySQL 8.4 integration test rejected a mutating assertion in a read-only
  transaction and verified that the seed row count and value were unchanged.
- Each variant produced one normalized run fingerprint with `flaky=false`.
- The smoke workflow checks the observed vulnerable and fixed replay markers.

### Pending evidence

The following evidence remains pending:

- differential Oracle evidence remains pending;
- schema constraint evidence remains pending;
- exploration beyond the recorded schedule remains pending; and
- rule `RG001` remains pending.

The executable SQL assertion supplies an invariant-based verdict for the
recorded vulnerable and locking paths. It does not provide a clean-run
differential, prove a schema constraint, or replace broader schedule
exploration.
