# ADR 0013: Scope a clean-run differential Oracle

- Status: Accepted
- Date: 2026-09-12
- Issue: [#115](https://github.com/weavegate/weavegate/issues/115)
- Implementation: [#126](https://github.com/weavegate/weavegate/issues/126)

## Context

The v0.2.0 milestone promises richer Oracles, but the first Spring race needs
only the zero-row SQL assertions already implemented. Adding another Oracle
without a distinct invariant would widen configuration, execution, diagnostic,
and artifact contracts without adding a verdict that users cannot express
today.

The existing boundaries separate two questions:

- [`oracle.Set`](../../internal/oracle/oracle.go) composes any number of Oracle
  implementations and preserves every pass or violation in the evaluation.
  Adding more independent post-state predicates therefore needs assertion data,
  not another composition mechanism.
- [`sqlassert`](../../internal/oracle/sqlassert/sqlassert.go) executes one fixed
  read-only query after the scheduled workers finish. It deliberately ignores
  `oracle.RunContext`, including the reserved `Golden` projection. It can decide
  any invariant encoded entirely in the current database state, but it cannot
  compare that state with the result of another execution.

The second synthetic invariant is: **a scheduled execution must produce the
same configured account projection as a declared clean serial execution of the
same scenario from the same reset state**. Consider two histories whose
scheduled projection is the identical row `{account_id: 1, balance: 90}`. If
the clean projection is also `90`, the differential passes; if the clean
projection is `100`, it violates. Every query over the identical scheduled
state has the same result in both histories, so a fixed post-state assertion
cannot distinguish them without copying one clean result into its SQL and
changing that SQL whenever the clean result changes.

The executable counterexample passes different `Golden` projections to the
current SQL Oracle while presenting the same post-state query result:

```bash
go test ./internal/oracle/sqlassert \
  -run '^TestSQLAssertionCannotCompareGoldenProjection$' -v -count=1
```

It records `differential_verdicts=pass,violation` while the SQL Oracle returns
`sql_verdicts=pass,pass`. This is an input-data limitation, not a missing SQL
predicate or a reason to weaken the existing assertion.

## Decision

Implement one clean-run differential Oracle as the v0.2.0 richer-Oracle
deliverable. The implementation is required before v0.2.0 is complete, but it
is not a prerequisite for the first Spring/CI vertical slice: that slice keeps
using existing SQL assertions. A bounded follow-up issue owns implementation;
#115 closes only the scope decision and executable limitation evidence.

### Execution and configuration boundary

The planned configuration adds a sibling collection rather than overloading
`oracle.assertions`:

```yaml
oracle:
  assertions:
    - id: active-assignment-is-unique
      sql: SELECT ...
      expect_rows: 0
  differentials:
    - id: account-outcome-matches-clean-run
      reference_schedule: sch_0123456789ab
      projection_sql: |
        SELECT account_id, balance FROM account ORDER BY account_id
      key_columns: [account_id]
```

`reference_schedule` resolves through the same content-addressed schedule
lookup and validation rules as `--replay`. For each candidate or replay repeat,
the engine resets the fixture, executes the reference schedule with the same
scenario, adapter, variant, parameters, and timeout policy, and captures the
configured read-only projection. It then resets again, executes the selected
schedule, and gives the captured projection to the Oracle through
`RunContext.Golden`. The selected run remains the run reported to the user;
reference-run failure, timeout, or incomplete terminal state is a run error and
cannot become a candidate violation or PASS.

The projection query follows the existing SQL assertion restrictions: one
read-only transaction, no locking read, and the shared run deadline. IDs must
be unique across assertions and differentials. `key_columns` must be nonempty,
unique, present in every row, and contain deterministic non-null scalar values.
Duplicate keys or unsupported column values make evaluation fail because a
row-to-row comparison would be ambiguous. Declaration order remains the
user-facing Oracle order and `oracle.Set` remains the only composition layer.

### Deterministic evidence and diagnostics

The Oracle canonicalizes both projections by the encoded key tuple and compares
non-key columns by sorted column name. It emits three existing planned anomaly
classes:

| Difference | `oracle.Violation.Kind` | Diagnostic trigger | Planned code |
| --- | --- | --- | --- |
| key exists only in the scheduled projection | `duplicate` | `oracle.duplicate` | `WG002` |
| key exists only in the clean projection | `missing` | `oracle.missing` | `WG003` |
| the same key has different non-key values | `stale` | `oracle.stale` | `WG004` |

Duplicate and missing evidence contains the complete normalized scheduled or
clean row respectively. Stale evidence contains each key plus paired
`expected_<column>` and `observed_<column>` values for changed columns. Column
names that would collide after prefixing are rejected during preflight. Rows
are ordered by canonical key, and changed fields by column name, before the
existing evaluation fingerprint is built. Repeating the same selected and
reference schedules must therefore produce the same verdict and evidence or
the existing flaky classification applies.

The implementation change must ship `rules/WG002.json`, `rules/WG003.json`,
and `rules/WG004.json` together with one-to-one reference pages at
`docs/reference/diagnostics/WG002.md`, `WG003.md`, and `WG004.md`. Diagnostic
derivation continues to name an Oracle verdict; neither the orchestrator nor a
fixture classifies the difference.

### Artifact compatibility

The implementation records the resolved differential declarations, including
the full reference schedule, projection SQL, and key columns, as an additive
field in `observation.json`. It records `duplicate_rows`, `missing_rows`, and
`stale_rows` as separate additive violation arrays; it does not reinterpret
`assertion_violations` or `oracles`. The ordinary artifact version remains 2
under [ADR 0007](0007-artifact-version-policy.md): these are additive fields
and consumers already must tolerate their absence. Version 3 remains reserved
for retained diagnostic-derivation failures.

The complete reference schedule is embedded in the deterministic observation
instead of adding an eighth file. A copied run directory therefore retains the
inputs needed to audit and resolve both executions without changing the public
six-or-seven-file artifact count.

## Consequences

- v0.2.0 has one concrete richer-Oracle outcome: detect duplicate, missing, and
  stale result rows relative to a declared clean serial execution.
- Fixtures with predicates that depend only on final state continue to compose
  zero-row assertions; they do not pay for a reference execution.
- Differential runs execute the SUT twice per candidate or repeat and reset
  between executions. This is a correctness cost, not hidden setup work, and
  the shared timeout/accounting contract must expose it.
- A reference schedule is declared evidence, not an automatically discovered
  proof of correctness. Its projection states the expected result for the
  selected invariant; reviewers must inspect it like any other Oracle input.
- Schema-constraint and fault-injection Oracles remain deferred. They answer
  different questions and are not implied by the v0.2.0 richer-Oracle promise.

## What didn't work

1. **Add another zero-row assertion.** Assertions already compose, and another
   post-state query sees identical input in the two-history counterexample.
2. **Place comparison logic in the orchestrator.** That would make execution
   decide a verdict and violate the Oracle boundary.
3. **Treat a fixed or Spring variant as the reference automatically.** Variant
   changes can alter domain behavior beyond concurrency control. The reference
   must be an explicit schedule over the same resolved SUT inputs.
4. **Implement schema-constraint inspection first.** Constraints provide useful
   defense-in-depth evidence, but they do not compare application outcomes and
   are unnecessary for the first Spring race.
