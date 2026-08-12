# Why the fix works

This page tracks the evidence that connects each weavegate fixture to a database
anomaly, an invariant check, and a verified fix. A row remains pending until the
repository contains an executable test and observable result for that stage.

## Coverage status

| Fixture | Anomaly | Fixture state | Controlled execution | Oracle | Fix evidence |
| --- | --- | --- | --- | --- | --- |
| [matching-slice](../fixtures/matching-slice/README.md) | Duplicate active assignment | Schema/seed ready | Test-controlled interleaving | Pending | Locking path observed |

## Matching slice

The matching-slice fixture models one active project request that may be linked
to matching sessions through assignment rows. Its invariant is that one active
project request has at most one active assignment.

The vulnerable schema deliberately permits multiple assignment rows for the
same request. This makes a duplicate state representable, but schema capability
alone is not evidence that a concurrent workflow has produced the anomaly. The
Go-native assignment test now executes that workflow through two worker-owned
database connections.

### Locking mechanism

Both variants run the same read-check-insert workflow inside a service-owned
transaction. The vulnerable variant reads the parent `project_request` without
a locking clause. Its two workers can both read the active request, observe no
active assignment, and reach `before_insert_assignment`. After the test barrier
releases them, each transaction inserts a matching session and an assignment.

The fixed variant reads the same parent row with `FOR UPDATE`. The first worker
holds that row lock while it checks and writes. The second worker's locking read
waits, so only the first worker reaches `before_insert_assignment`. Once the
first transaction commits, the second locking read continues and its subsequent
check sees the committed active assignment. The second command then returns the
normal already-assigned result without creating another session or assignment.

### Verified today

- MySQL 8.4 creates the three fixture tables with the InnoDB engine.
- Assignment rows reference existing project requests and matching sessions.
- The project-request assignment index is not unique.
- Reset restores request `42` and removes all sessions and assignments.
- The reset behavior is exercised by the repository's smoke workflow.
- Each concurrent worker executes on its own database connection and preserves
  its worker ID through the coordination seam.
- The vulnerable path produced two sessions and two active assignments after
  both workers reached `before_insert_assignment`.
- The `FOR UPDATE` path produced one session and one active assignment; both
  workers completed without a command error.
- The smoke workflow checks the observed vulnerable and fixed result markers.

### Pending evidence

The following evidence is not implemented yet:

- a reusable SQL Oracle that evaluates the active-assignment invariant;
- engine-driven orchestration of the named coordination points; and
- a saved schedule artifact that can drive the workflow independently of its
  test-local barrier.

The coverage table keeps the Oracle pending until the repository contains that
executable invariant check. The current fix evidence is limited to the locking
path and result counts exercised by the matching-slice integration test.
