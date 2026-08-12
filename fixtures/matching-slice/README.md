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

The default coordination implementation is a no-op. The integration test uses
a test-local barrier at `before_insert_assignment` to compare two workers on
the same request.

Run the sequential and concurrent SUT tests with Docker available:

```bash
go test ./fixtures/matching-slice/sut -run TestAssignSequential -v -count=1
go test ./fixtures/matching-slice/sut -run TestAssignConcurrent -v -count=1
```

The concurrent test has observed these result markers:

```text
SUT_ASSIGN_RESULT variant=vulnerable workers=2 errors=0 sessions=2 assignments=2 active_assignments=2 duplicate=true barrier=all_arrived worker_identity=preserved
SUT_ASSIGN_RESULT variant=fixed workers=2 errors=0 sessions=1 assignments=1 active_assignments=1 duplicate=false barrier=timeout worker_identity=preserved
```

## Current boundary

This fixture verifies the schema, seed and reset behavior, an application
assignment workflow, and a test-controlled comparison of plain and locking
reads. The row counts above are assertions inside that integration test. A
reusable invariant Oracle and engine-driven orchestration are not implemented
yet.

The evidence status is tracked in
[Why the fix works](../../docs/why-the-fix-works.md).
