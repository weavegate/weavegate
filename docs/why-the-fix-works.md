# Why the fix works

This page tracks the evidence that connects each weavegate fixture to a database
anomaly, an invariant check, and a verified fix. A row remains pending until the
repository contains an executable test and observable result for that stage.

## Coverage status

| Fixture | Anomaly | Fixture state | Controlled execution | Oracle | Fix evidence |
| --- | --- | --- | --- | --- | --- |
| [matching-slice](../fixtures/matching-slice/README.md) | Duplicate active assignment | Schema/seed ready | Saved schedule replay (20/20) | Pending | Locking replay 20/20 |

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
- The vulnerable replay produced two sessions and two active assignments in
  20/20 runs after both workers reached `before_insert_assignment`.
- The `FOR UPDATE` path paired the runtime's timeout inference with a separate
  InnoDB row-lock wait observation, then resumed the second worker.
- The locking replay produced one session and one active assignment in 20/20
  runs; both workers completed without a command error, and the second worker
  did not visit `before_insert_assignment`.
- Each variant produced one normalized state fingerprint with `flaky=false`.
- The smoke workflow checks the observed vulnerable and fixed replay markers.

### Pending evidence

The following evidence remains pending:

- a reusable SQL Oracle that evaluates the active-assignment invariant;
- exploration beyond the recorded schedule; and
- rule `RG001`, which depends on the reusable Oracle.

The coverage table keeps the Oracle pending until the repository contains that
executable invariant check. Saved schedule replay supplies repeat evidence for
the recorded vulnerable and locking paths, but it does not replace broader
schedule exploration or an invariant-based verdict.
