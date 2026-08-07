# Why the fix works

This page tracks the evidence that connects each weavegate fixture to a database
anomaly, an invariant check, and a verified fix. A row remains pending until the
repository contains an executable test and observable result for that stage.

## Coverage status

| Fixture | Anomaly | Fixture state | Controlled execution | Oracle | Fix evidence |
| --- | --- | --- | --- | --- | --- |
| [matching-slice](../fixtures/matching-slice/README.md) | Duplicate active assignment | Schema/seed ready | Pending | Pending | Pending |

## Matching slice

The matching-slice fixture models one active project request that may be linked
to matching sessions through assignment rows. Its invariant is that one active
project request has at most one active assignment.

The vulnerable schema deliberately permits multiple assignment rows for the
same request. This makes a duplicate state representable, but schema capability
alone is not evidence that a concurrent workflow has produced the anomaly.

### Verified today

- MySQL 8.4 creates the three fixture tables with the InnoDB engine.
- Assignment rows reference existing project requests and matching sessions.
- The project-request assignment index is not unique.
- Reset restores request `42` and removes all sessions and assignments.
- The reset behavior is exercised by the repository's smoke workflow.

### Pending evidence

The following evidence is not implemented yet:

- an application workflow with two workers reading and assigning request `42`;
- controlled sync-points that force both workers through a violating order;
- a SQL oracle that reports more than one active assignment for the request;
- replay of the same schedule against a locking or constraint-based fix; and
- a repeated result showing that the violating and fixed outcomes are stable.

Until those checks exist, this page does not claim that a duplicate assignment
has been observed or that a particular fix has been verified. The mechanism and
evidence will be documented here when the executable workflow is added.
