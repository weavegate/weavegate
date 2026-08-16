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
| `run_id` | string | `run_<YYYYMMDDTHHMMSS.mmmZ>_<8 hex>`. The fixed-width millisecond field means lexicographic order equals time order — `weavegate report` with no run ID relies on exactly this to pick "most recent" unambiguously, including two runs started in the same second. |
| `started_at` | string (RFC3339, UTC) | |
| `weavegate_version` | string | `0.0.0-dev` unless built with `-ldflags "-X main.version=..."`. |
| `schema_version` | string | First 12 hex characters of the sha256 of every migration file's `"<basename>\n"+content`, concatenated in sorted filename order. Content-derived, not invented. |
| `seed_data` | string | First 12 hex characters of the sha256 of the seed file's content. |
| `isolation_level` | string | The literal result of `SELECT @@global.transaction_isolation`, read from the fixture's managing connection immediately after provisioning — the **global** value, not the session value a worker connection might override. If the SUT changes its session isolation level, this field will not reflect that. |
| `engine` | string | Distinct storage engines actually in use by the provisioned schema's tables, read from `information_schema.tables` immediately after provisioning and joined with `,` when more than one engine is present. Not assumed from the config or migrations. |
| `adapter`, `variant`, `image` | string | The resolved adapter, SUT variant, and database image for this run. |

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

## `observation.json`

| Field | Type | Notes |
| --- | --- | --- |
| `schedules_explored` | int | The number of candidates **actually evaluated**, not the total size of the candidate space. Explore mode stops at the first violation, so this is often smaller than the full candidate count. |
| `explore_passes` | int | How many full sweeps ran before stopping — 1 if a violation was found on the first pass, up to `run.explore_passes` if every pass exhausted its candidates. `0` in replay mode, where no exploration happens. |
| `assertion_violations` | array of strings | IDs of assertions that had at least one violation across the replay runs. `[]` when none did. |
| `repeat` | int | The effective repeat count used (config default or `--repeat` override). |
| `violation_runs` | int | How many of the `repeat` replay runs had at least one violation. |
| `flaky` | bool | See [exit-codes.md](exit-codes.md#the-flaky-determination). |

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

The underlying trace model is deliberately wall-clock-free — there is no
`t_ms` field on an event. A timeline view that needs one is a future
`report --timeline` feature, not part of this schema.

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
