# weavegate

> Deterministically replay the DB race, gate the deploy.

**weavegate** is a CI gate for schedule-dependent database bugs. It plants test-only sync-points into the `read -> decide -> write` paths of a Spring Boot + MySQL/InnoDB workflow, deterministically reproduces the exact execution orders (schedules) that break your state invariants, and turns violations into evidence — a saved schedule, a step trace, and the offending rows — right in your pull request.

![status](https://img.shields.io/badge/status-in%20design-orange)
![license](https://img.shields.io/badge/license-Apache--2.0-blue)
![db](https://img.shields.io/badge/DB-MySQL%208%20%2F%20InnoDB-lightgrey)

> ⚠️ **Pre-release.** weavegate is under active development. The first runnable release (`v0.1.0-alpha`) is targeted for August 2026. Everything below describes the designed behavior; interfaces may change until then.

## The problem

Two requests arrive at almost the same time — and the same record ends up claimed, assigned, or counted twice. Code review passed. Integration tests passed. The bug only appears under one specific interleaving of transactions, so ordinary tests hit it rarely, flakily, or never. And when a test *does* hit it, nothing about **which execution order broke it** survives as evidence in the PR.

The database is not at fault — MySQL is behaving *as documented*: its own locking-reads documentation tells you these schedules exist, and Jepsen's MySQL analysis catalogs exactly which anomalies Repeatable Read still permits. Whichever side of the isolation-naming debate you take, your workflow runs on top of the schedules the DB actually permits — and whether it survives them is **your application's problem**. That is the layer weavegate tests.

## How it works

```text
 your workflow code          weavegate
┌─────────────────────┐     ┌──────────────────────────────────────────┐
│ read request        │     │ 1. explore release-order schedules       │
│   ● sync-point ─────┼────▶│ 2. force the interleaving, every time    │
│ decide              │     │ 3. check SQL oracles on the real DB      │
│   ● sync-point ─────┼────▶│ 4. violation → error[RG001] + evidence   │
│ write assignment    │     │ 5. save the schedule → replay on demand  │
└─────────────────────┘     └──────────────────────────────────────────┘
```

- **Controlled replay** — named sync-points let weavegate control worker execution order. A violating schedule is saved as an artifact and can be re-run exactly: same schema, same seed, same schedule, same result (`repeat=20`, `flaky=false`).
- **SQL oracles** — you declare domain invariants as plain SQL assertions; a clean-run differential and schema constraints back them up. No DSL to learn.
- **Compiler-style diagnostics** — violations render as `error[RG001]` with observed state, broken invariant, likely reason, and suggested fixes.
- **CI gate** — exit codes, `report.json`/`report.md`, `trace.json`, a one-line GitHub Action, and a PR comment with the replay command.

A diagnostic produced by the matching-slice run, end to end:

```text
## weavegate: FAIL (RG001)
scenario: concurrent-assign | schedules explored: 1 | violating: sch_7dcb74b1e506
assertion: active-assignment-is-unique
flaky: false (repeat=20)
replay: weavegate run --config fixtures/matching-slice/.weavegate/config.yaml --scenario concurrent-assign --variant vulnerable --replay sch_7dcb74b1e506 --repeat 20

error[RG001]: invariant violated under a controlled schedule
  observed:  active-assignment-is-unique returned 1 row: active_assignment_count=2 project_request_id=42
  assertion: active-assignment-is-unique
  invariant: a declared state invariant must hold under every release schedule the database permits
  reason:    commonly a read-then-write path without a lock or a unique constraint
  help:      add a unique constraint on the contested key
             take a pessimistic lock (SELECT ... FOR UPDATE) before insert
             use an idempotency key on the write
  evidence:  schedule sch_7dcb74b1e506 · trace.json · 1 violating row
```

See the full [RG001 reference](docs/reference/diagnostics/RG001.md), including
what this code does not claim.

After you fix the code (unique constraint or `SELECT ... FOR UPDATE`), replaying the **same schedule** passes — the gate verifies the fix itself, not just the happy path.

## What weavegate is (and is not)

- It is **not** a fuzzer that finds every race automatically — it explores interleavings between sync-points *you* place at suspicious `read -> decide -> write` hot spots.
- It is **not** a database engine tester (that's Hermitage/Jepsen territory) — it tests whether **your workflow** survives the anomalies your database legitimately permits, and whether your fix closes them.
- It does **not** verify ACID — it trusts the DB's ACID and isolation guarantees, and checks your invariants on top of them.
- There is **no AI verdict** — judgments are rule-based (SQL oracles + differential + trace), so every failure is deterministic and reproducible by anyone with one command.

## Roadmap

| Milestone | Scope | Target |
| --- | --- | --- |
| `v0.1.0-alpha` | Go-native engine end-to-end: sync-point runtime, schedule exploration & replay, SQL/differential/schema oracles, `RG001` diagnostics, CLI, report/trace artifacts | Aug 2026 |
| `v0.2.0` | Spring Boot test-slice adapter (`ReplayPoint`, no-op in production), one-line GitHub Action + PR comment, one-command demo (`weavegate demo init`) | Aug 2026 |
| later | second fixture (job-claim), abort-then-retry recoverability, isolation-level matrix (RC vs RR), `data_lock_waits`-based lock-wait detection | Q3–Q4 2026 |

## Built on / related work

weavegate runs on [Testcontainers](https://testcontainers.com/) (real MySQL 8/InnoDB, not mocks) and draws design ideas from the PostgreSQL isolation tester, [Hermitage](https://github.com/ept/hermitage), [Lincheck](https://github.com/JetBrains/lincheck), and the replay thinking of deterministic-simulation testing (FoundationDB, TigerBeetle). It applies none of them as-is: those tools ask *"does the DB permit this anomaly?"* — weavegate asks *"does your application code survive the anomalies the DB permits, and does your fix close them?"* A full attribution list will ship in `docs/related-work.md`.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for how to build the project, run the checks, and open a pull request; adding a fixture is the most welcome way to contribute. Repository working conventions live in [`AGENTS.md`](AGENTS.md), conduct is governed by [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md), and vulnerabilities are reported through [`SECURITY.md`](SECURITY.md). Questions and usage discussion go to [GitHub Discussions](https://github.com/weavegate/weavegate/discussions) rather than an issue.

## License

Apache-2.0.
