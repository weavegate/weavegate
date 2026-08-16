# Report schema reference

Every `weavegate run` — pass or violation — writes six files to
`<out>/runs/<run_id>/`:

```text
<out>/runs/<run_id>/
├── manifest.json      (volatile)
├── scenario.json       (deterministic)
├── observation.json    (deterministic)
├── trace.json          (deterministic)
├── report.json         (volatile)
└── report.md           (deterministic)
```

## Volatile vs. deterministic

Two files carry per-run identity and are expected to differ between two runs
of the same config: `manifest.json` (run ID, start time) and `report.json`
(a merge of manifest, scenario, and observation, so it inherits manifest's
volatile fields). The remaining four files are **byte-identical** for two
runs given the same config content and the same CLI flags. Two identical
runs with different volatile fields but identical deterministic files is the
expected, correct outcome — see
[docs/adr/0005-volatile-run-metadata-boundary.md](../adr/0005-volatile-run-metadata-boundary.md)
for why the boundary sits here rather than, say, keeping every file
deterministic or stripping timestamps everywhere.

## `manifest.json`

| Field | Type | Notes |
| --- | --- | --- |
| `run_id` | string | Opaque identity: `run_<YYYYMMDDTHHMMSS.nnnnnnnnnZ>_<32 lowercase hex>`. The timestamp is readable context and the suffix is 128 random bits; ordering comes from `started_at`, with run ID used only to break identical-time ties. |
| `started_at` | string (RFC3339, UTC) | |
| `weavegate_version` | string | `0.0.0-dev` unless built with `-ldflags "-X main.version=..."`. |
| `schema_version` | string | Full `sha256:<64 lowercase hex>` of the prepared migration snapshot. Each sorted file is framed as decimal byte length of name + LF, name bytes, decimal byte length of content + LF, content bytes. |
| `seed_data` | string | Full `sha256:<64 lowercase hex>` of the prepared seed bytes. |
| `isolation_level` | string | The literal result of `SELECT @@global.transaction_isolation`, read from the fixture's managing connection immediately after provisioning — the **global** value, not the session value a worker connection might override. If the SUT changes its session isolation level, this field will not reflect that. |
| `engine` | string | Distinct storage engines actually in use by the provisioned schema's tables, read from `information_schema.tables` immediately after provisioning and joined with `,` when more than one engine is present. Not assumed from the config or migrations. |
| `adapter`, `variant`, `image` | string | The resolved adapter, SUT variant, and database image for this run. |
| `cleanup_failed` | bool | `true` when fixture teardown failed after this run completed (see [exit-codes.md](exit-codes.md#cleanup-failures-never-mask-a-verdict) for how this can raise a passing run's exit code to 4). It never changes `report.md`'s headline or `scenario.json`/`observation.json`'s content — those describe the scenario's own, deterministic outcome — but it records, on the run this actually happened to, why the process exit code can be higher than the headline alone would suggest. `false` on every ordinary run. |

Both digests come from the same immutable prepared source that `Provision` and
every `Reset` apply. The CLI does not re-read migration or seed paths while
building the manifest.

## `scenario.json`

The configured scenario (`name`, `workers`, `sync_points`, `params`) plus
`violating_schedule` — the schedule this run reports on: the schedule
exploration discovered, or the schedule a replay was given. `null` (the key
is omitted) when the run passed with nothing to report.

`params` is the effective worker `args` from the config (every worker in a
scenario must declare the same `args`, so one map covers all of them; `{}`
when none are set). It is recorded here, not only in the config, so the
evidence still shows which parameters produced this verdict even if the
referenced config is later edited or deleted.

Every field here is `internal/report`'s own JSON contract, not the engine's
internal types re-serialized — the report package defines its own
`Worker`/`Schedule`/`CoordinationStep` shapes and converts into them, so this
schema stays independent from unrelated changes to the engine's internal
field names or casing:

```json
{
  "name": "concurrent-assign",
  "workers": [{"id": "w1", "command": "assign"}, {"id": "w2", "command": "assign"}],
  "sync_points": ["after_read_request", "before_insert_assignment"],
  "params": {"request_id": "42"},
  "violating_schedule": {
    "id": "sch_7dcb74b1e506",
    "steps": [{"worker": "w1", "point": "after_read_request"}, "..."]
  }
}
```

## `observation.json`

| Field | Type | Notes |
| --- | --- | --- |
| `schedules_explored` | int | The number of candidates **actually evaluated**, not the total size of the candidate space. Explore mode stops at the first violation, so this is often smaller than the full candidate count. |
| `explore_passes` | int | How many full sweeps ran before stopping — 1 if a violation was found on the first pass, up to `run.explore_passes` if every pass exhausted its candidates. `0` in replay mode, where no exploration happens. |
| `assertion_violations` | array of objects | One entry per distinct violation found across the replay runs, plus exploration's own discovery run when replay never reproduced it (the 0/repeat flaky case — see `flaky` below). Each entry is `{"oracle_id": "...", "rows": [...]}` — `oracle_id` names the assertion, and `rows` is that oracle's own evidence rows, so a saved verdict shows *which* rows violated it, not only that it did. Each row is a plain object keyed by the query's column names, restricted to deterministic, JSON-safe scalar values (no `NaN`/`Inf`, no non-UTF-8 strings). Two violations sharing an `oracle_id` are only collapsed into one entry when their rows are also identical; a later run reproducing the same assertion with different rows is kept as a separate entry so each stays individually auditable. `[]` when nothing violated. |
| `oracles` | array of objects | Every configured assertion's effective `id`, `sql`, and `expect_rows` — the same fields as `oracle.assertions` in config, snapshotted here so a saved verdict stays auditable against the query that produced it even after the referenced config is edited or deleted. `[]` when the scenario declares none. |
| `repeat` | int | The effective repeat count used (config default or `--repeat` override). |
| `timeouts` | object | The effective arrive timeout and its four derived orchestrator deadlines, in milliseconds: `arrive_timeout_ms` (config default or resolved value), `block_inference_timeout_ms` (equal to `arrive_timeout_ms`), `step_timeout_ms` (20×), `run_timeout_ms` (60×), `stop_timeout_ms` (20×) — see [Timing](config.md#timing) for how these are derived. These deadlines affect terminal timing classifications and normalized fingerprints, and therefore potentially the `flaky` verdict, so they are snapshotted here even after the referenced config is edited or deleted. |
| `violation_runs` | int | How many of the `repeat` replay runs had at least one violation. |
| `flaky` | bool | See [exit-codes.md](exit-codes.md#the-flaky-determination). |
| `fingerprints` | object (string → int) | How many of the `repeat` replay runs produced each distinct normalized execution fingerprint. A run can be flaky purely from this divergence (differing terminal states or timing classification) with zero assertion violations anywhere, so this is the only evidence of *why* such a run is flaky. One entry, equal to `repeat`, when every run agreed. |

```json
"assertion_violations": [
  {
    "oracle_id": "active-assignment-is-unique",
    "rows": [{"project_request_id": 42, "active_assignment_count": 2}]
  }
]
```

**Fields not emitted yet:** `duplicate_rows`, `missing_rows`, `stale_rows`,
`constraint_violations`, `aborted_transactions`, `retries`, and
`recoverable` are not present in this file. They belong to oracles
(differential, schema-constraint, fault injection) that are not implemented.
They are omitted rather than filled with `0` or `[]`, because a `0` cannot be
told apart from "not measured" — printing them would claim a check that
never ran.

## `trace.json`

```json
{"schedule_ref": "...", "events": [...], "terminals": [...]}
```

Like `scenario.json`, this is `internal/report`'s own `Event`/`WorkerTerminal`
shapes, converted from the engine's internal trace types rather than
re-serializing them directly.

The underlying trace model is deliberately wall-clock-free — there is no
`t_ms` field on an event. A timeline view that needs one is a future
`report --timeline` feature, not part of this schema.

The saved run is whichever run best supports the reported observation: a
replay run with an assertion violation when one exists; failing that,
exploration's own discovery run when replay never reproduced it (the
0/repeat flaky case); failing that, a replay run whose fingerprint
diverged from the others (a direct `--replay` can be flaky purely from
execution-fingerprint divergence, with zero assertion violations anywhere —
see `fingerprints` above); and only the first replay run when none of the
above found anything to show.

## `report.json`

`{"manifest": ..., "scenario": ..., "observation": ...}` — the three files
above merged into one, for a reader who wants everything in a single fetch.
Because it inherits `manifest`'s volatile fields, it is not part of the
deterministic set even though `scenario` and `observation` alone would be.

## `report.md`

```text
## weavegate: FAIL
scenario: concurrent-assign | schedules explored: 1 | violating: sch_7dcb74b1e506
assertion: active-assignment-is-unique
flaky: false (repeat=20)
replay: weavegate run --config fixtures/matching-slice/.weavegate/config.yaml --scenario concurrent-assign --variant vulnerable --replay sch_7dcb74b1e506 --repeat 20
```

The headline is `PASS`, `FAIL`, or `FLAKY` — matching the exit code's three
verdict outcomes (0, 2, 3; see [exit-codes.md](exit-codes.md)). It does not
include a diagnostic code yet; that is a future `RG`-code renderer, not part
of this schema. `PASS` output has no `replay:` line, since there is no
schedule to reproduce:

```text
## weavegate: PASS
scenario: concurrent-assign | schedules explored: 18 (exhausted) | violating: none
flaky: false (repeat=20)
```

The `replay:` line, when present, is a complete command — every value it
needs (`--config` exactly as the user passed it, `--scenario`, `--variant`,
`--replay`, `--repeat`) is spelled out, so pasting it verbatim from the same
working directory reproduces the same verdict without reconstructing any
argument by hand. `--config` is never normalized to an absolute path, so
this file stays byte-identical between two runs regardless of where in the
filesystem they happened to execute.

`--out` is deliberately absent from this line: it is not part of the
deterministic contract (two runs with different `--out` values must still
produce byte-identical `report.md`), so pasting the replay line uses `--out`'s
default (or whatever `--out` the paste is run with) rather than the value the
original run happened to use. A reader replaying from the same directory as
the original run — the common case — still finds the schedule through stage
① of `--replay` resolution (see [cli.md](cli.md#--replay-resolution-order));
replaying from a different `--out` falls back to stage ②, which only
resolves schedules the entrypoint has registered.

## File and directory modes

Directories are written `0755`, files `0644`. Writing uses a same-filesystem
temporary directory under `<out>/runs/` and an atomic rename, so a partial
failure during writing never leaves a half-written run directory behind —
and never leaves nothing behind either: on success, all six files exist
together or none do.
