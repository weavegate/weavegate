# weavegate

> Deterministically replay the DB race, gate the deploy.

Your integration tests hit this bug by luck. **weavegate** hits it every time — and proves your fix closes it. This replay gate currently runs against a Go-native reference SUT; a Spring Boot adapter is planned for `v0.2.0`.

[![smoke](https://github.com/weavegate/weavegate/actions/workflows/smoke.yml/badge.svg?branch=main)](https://github.com/weavegate/weavegate/actions/workflows/smoke.yml?query=branch%3Amain)
![license](https://img.shields.io/badge/license-Apache--2.0-blue)
![db](https://img.shields.io/badge/DB-MySQL%208%20%2F%20InnoDB-lightgrey)
![coverage](https://img.shields.io/badge/coverage-88%25-brightgreen)

> ⚠️ **Pre-release.** The `weavegate run` and `weavegate report` commands can be
> run from a source checkout or a release archive that includes the reference
> fixture data. Interfaces may change throughout the pre-1.0 series.

## Prerequisites

- A running Docker daemon; Testcontainers starts a real MySQL 8.4 container.
- Go 1.25 or later, as declared in [`go.mod`](go.mod), only when building the
  CLI from source. Release archives contain a statically built binary.
- Allow roughly 35 seconds for the
  [documented exploration test](docs/experiments/exploration.md#reproduction)
  on the measured development host; an initial image pull can add time. See
  [Contributing](CONTRIBUTING.md#running-checks) for the build and test commands.

### From source

Build the CLI and put the checkout-local binary on `PATH`:

```bash
go build -o weavegate ./cmd/weavegate
export PATH="$PWD:$PATH"
```

### From a release archive

Extract the archive and, from its top-level directory, put the bundled binary
on `PATH`; the matching-slice config, migration, and seed data are already in
place:

```bash
tar -xzf weavegate_*.tar.gz
cd weavegate_*/
export PATH="$PWD:$PATH"
```

The archive contains the quick-start fixture. Full [documentation](docs/README.md) and
the sources for relative references remain in the
[weavegate repository](https://github.com/weavegate/weavegate).

### Run the CLI

The vulnerable variant intentionally ends with exit 2 because the SQL assertion
finds and reproduces the invariant violation.

Run the CLI from either prepared directory:

```console
$ weavegate run --config fixtures/matching-slice/.weavegate/config.yaml \
    --scenario concurrent-assign --variant vulnerable
## weavegate: FAIL (WG001)
scenario: concurrent-assign | schedules explored: 1 | violating: sch_7dcb74b1e506
assertion: active-assignment-is-unique
flaky: false (repeat=20)
replay: weavegate run --config fixtures/matching-slice/.weavegate/config.yaml --scenario concurrent-assign --variant vulnerable --replay sch_7dcb74b1e506 --repeat 20

error[WG001]: invariant violated under a controlled schedule
  observed:  active-assignment-is-unique returned 1 row: active_assignment_count=2 project_request_id=42
  assertion: active-assignment-is-unique
  invariant: a declared state invariant must hold under every release schedule the database permits
  reason:    commonly a read-then-write path without a lock or a unique constraint
  help:      add a unique constraint on the contested key
             take a pessimistic lock (SELECT ... FOR UPDATE) before insert
             use an idempotency key on the write
  evidence:  schedule sch_7dcb74b1e506 · trace.json · observation.json · 1 violating row
.weavegate/runs/run_20260815T172917.706000000Z_bc391ac51234567890abcdef12345678
$ echo $?
2
```

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
│   ● sync-point ─────┼────▶│ 4. violation → error[WG001] + evidence   │
│ write assignment    │     │ 5. save the schedule → replay on demand  │
└─────────────────────┘     └──────────────────────────────────────────┘
```

- **Controlled replay** — named sync-points let weavegate control worker execution order. A violating schedule is saved as an artifact and can be re-run exactly: same schema, same seed, same schedule, same result (`repeat=20`, `flaky=false`).
- **SQL assertion oracles** — you declare domain invariants as plain SQL assertions and retain the violating rows with the execution trace. No DSL to learn.
- **Compiler-style diagnostics** — violations render as `error[WG001]` with observed state, broken invariant, likely reason, and suggested fixes.
- **CI evidence** — exit codes, `report.json`/`report.md`, and `trace.json` provide the verdict and its supporting artifacts today.
- **Planned CI integration** — the `v0.2.0` roadmap adds a one-line GitHub Action and a PR comment with the replay command.

See the full [WG001 reference](docs/reference/diagnostics/WG001.md), including
what this code does not claim.

After you fix the path with `SELECT ... FOR UPDATE`, replaying the **same
schedule** recorded in the
[baseline comparison](docs/experiments/baseline-comparison.md#measured-result)
passes in the
[fixed-variant measurement](docs/experiments/determinism.md#repeated-result) —
the gate verifies your implemented fix, not just the happy path.

## What weavegate is (and is not)

- It is **not** a fuzzer that finds every race automatically — it explores interleavings between sync-points *you* place at suspicious `read -> decide -> write` hot spots.
- It is **not** a database engine tester (that's Hermitage/Jepsen territory) — it tests whether **your workflow** survives the anomalies your database legitimately permits, and whether your fix closes them.
- It does **not** verify ACID — it trusts the DB's ACID and isolation guarantees, and checks your invariants on top of them.
- There is **no AI verdict** — judgments are rule-based (SQL assertion oracles + trace), so every failure is deterministic and reproducible by anyone with one command.

See the documented [limitations](docs/limitations.md) for the exact boundaries
of these claims.

## Roadmap

| Milestone | Scope | Target |
| --- | --- | --- |
| `v0.1.0-alpha` | Go-native engine end-to-end: sync-point runtime, schedule exploration & replay, SQL assertion oracle, `WG001` diagnostics, CLI, report/trace artifacts | Aug 2026 |
| `v0.2.0` | Spring Boot test-slice adapter (`ReplayPoint`, no-op in production), differential/schema oracles, one-line GitHub Action + PR comment, one-command demo (`weavegate demo init`) | Sep 2026 |
| later | second fixture (job-claim), abort-then-retry recoverability, isolation-level matrix (RC vs RR), `data_lock_waits`-based lock-wait detection | Q3–Q4 2026 |

## Built on / related work

weavegate runs on [Testcontainers](https://testcontainers.com/) (real MySQL 8/InnoDB, not mocks) and draws design ideas from the PostgreSQL isolation tester, [Hermitage](https://github.com/ept/hermitage), [Lincheck](https://github.com/JetBrains/lincheck), and the replay thinking of deterministic-simulation testing (FoundationDB, TigerBeetle). It applies none of them as-is: those tools ask *"does the DB permit this anomaly?"* — weavegate asks *"does your application code survive the anomalies the DB permits, and does your fix close them?"* See the full [related-work and attribution](docs/related-work.md).

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for how to build the project, run the checks, and open a pull request; adding a fixture is the most welcome way to contribute. Repository working conventions live in [`AGENTS.md`](AGENTS.md), conduct is governed by [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md), and vulnerabilities are reported through [`SECURITY.md`](SECURITY.md). Questions and usage discussion go to [GitHub Discussions](https://github.com/weavegate/weavegate/discussions) rather than an issue.

## License

Apache-2.0.
