# Architecture

Weavegate separates control, application execution, and judgment. Fixtures and
adapters describe what to run; the orchestrator controls when workers advance;
oracles alone decide whether the resulting state is acceptable.

## Execution flow

```text
 config + scenario + schedule
              |
              v
     +------------------+       provision/reset       +---------+
     |   orchestrator   | <--------------------------> | fixture |
     +--------+---------+                              +---------+
              |
       invoke | workers                    application boundary
              v
        +-----------+        Arrive         +-------------------+
        |  adapter  | --------------------> | sync-point runtime|
        +-----+-----+ <-------------------- +---------+---------+
              |              release                  ^
              | application commands                  |
              v                                       |
        +-----------+                                 |
        |    SUT    |                 control --------+
        +-----+-----+
              |
       terminal state + normalized trace
              v
        +-----------+       violations       +------------------+
        |  oracles  | ---------------------> | diagnostic rules |
        +-----+-----+                         +--------+---------+
              |                                       |
              +---------------+-----------------------+
                              v
                       +-------------+
                       |   reports   |
                       +-------------+
```

The fixture package provisions and resets MySQL from an immutable migration
and seed snapshot. The adapter starts the selected application integration and
invokes worker commands on dedicated connections. The orchestrator executes a
validated schedule through the sync-point runtime and waits for every worker's
terminal state before passing normalized evidence to the oracle set. Diagnostic
rules classify violations, and the report package writes the public artifacts.

## Boundaries

| Boundary | Owns | Must not own |
| --- | --- | --- |
| Fixture | Synthetic schema, seed, reset behavior, scenario data | Engine control flow or verdict logic |
| Adapter | Starting and stopping a SUT; asynchronous worker invocation | Schedule policy or invariant judgment |
| Sync-point runtime | Named arrival, targeted release, and worker lifecycle state | Database assertions |
| Orchestrator | Exploration/replay execution, deadlines, normalized trace and terminal collection | Fixture-specific business rules or verdicts |
| Oracle | Post-run invariant evaluation and deterministic evidence rows | Worker release order or fixture provisioning |
| Diagnostic rules | Stable code, explanation, and help text for a derived violation kind | Discovering violations |
| Report | Conversion to the documented artifact schema and Markdown rendering | Re-evaluating the run |

The public [report schema](reference/report-schema.md) is owned by the report
package rather than being a serialization of internal engine structs. The
adapter contract is visible in [`internal/sut`](../internal/sut/sut.go), and
the oracle contract in [`internal/oracle`](../internal/oracle/oracle.go).

## Extension points

| Extension | Add | Keep unchanged |
| --- | --- | --- |
| Fixture | Synthetic data, workflow commands, scenarios, and evidence tests under `fixtures/` | Orchestrator and oracle protocols |
| Adapter | An implementation of the SUT adapter/handle protocol plus composition wiring | Scheduling and report packages |
| Oracle | An implementation of the oracle interface and its evidence normalization | Orchestrator verdict logic, because none belongs there |
| Schedule strategy | Candidate enumeration behind the scenario strategy contract | Fixture workflows and oracle evaluation |
| Diagnostic | One embedded rule and its one-to-one reference page | Oracle semantics and report schema |

These are extension points, not promises of runtime plugin loading. The current
CLI supports only its registered `matching-slice` entrypoint and Go-native
adapter, as documented in the
[configuration reference](reference/config.md#built-in-entrypoints).

Adding a fixture, oracle, schedule strategy, or adapter should not require a
change to an engine package. If it does, treat that as a missing boundary and
raise an issue instead of placing fixture-specific behavior in the engine.
