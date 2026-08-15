# Configuration reference

`weavegate run --config <path>` reads one YAML file. Decoding is strict:
an unrecognized key — including a typo — is rejected rather than silently
ignored.

## Keys

| Key | Type | Required | Default | Notes |
| --- | --- | --- | --- | --- |
| `target.db` | string | yes | — | Must start with `mysql:`; only MySQL is supported today. |
| `target.schema.migrations` | string (path) | yes | — | Directory of `*.sql` migration files, applied in filename order. Relative to the config file's own directory, not the current working directory. |
| `target.schema.seed` | string (path) | yes | — | Seed SQL file, applied after migrations. Same path-resolution rule as `migrations`. |
| `target.sut.adapter` | string | yes | — | Must be `gonative`; `springtest` is not implemented yet. |
| `target.sut.entrypoint` | string | yes | — | A built-in entrypoint ID, **not a path** (see [Built-in entrypoints](#built-in-entrypoints)). A value containing `/` or `.` is rejected. |
| `target.sut.variant` | string | yes | — | Must be one of the entrypoint's declared variants (for `matching-slice`: `vulnerable` or `fixed`). Overridable with `--variant`. |
| `scenarios.<name>.workers` | list | yes, ≥1 | — | Each worker has `id`, `command`, and `args` (see [Worker args](#worker-args)). |
| `scenarios.<name>.sync_points` | list of strings | yes, ≥1 | — | The sync-point order every worker is coordinated against. |
| `oracle.assertions` | list | yes, ≥1 | — | Each assertion has `id`, `sql`, and `expect_rows`. |
| `oracle.assertions[].id` | string | yes | — | Must match `^[a-z0-9][a-z0-9-]*$`, and be unique within the list. |
| `oracle.assertions[].expect_rows` | int | yes | — | Must be `0`. The only implemented oracle is a zero-row SQL assertion. |
| `run.repeat` | int | no | `20` | How many times a schedule is replayed to check determinism. Must be positive if set. |
| `run.arrive_timeout_ms` | int | no | `3000` | See [Timing](#timing) below — this value has an outsized effect on run duration. |
| `run.explore_passes` | int | no | `3` | How many full candidate sweeps explore mode runs before declaring PASS. See [exit-codes.md](exit-codes.md) for what a PASS across N passes actually claims. |

`report:` is not a supported section yet — it is reserved for a future
PR-comment feature. This CLI's artifact contract is fixed (six files, always
written — see [report-schema.md](report-schema.md)), so a `report:` key is
rejected the same way any other unknown key is.

## Built-in entrypoints

`target.sut.entrypoint` selects a Go adapter compiled into the binary — it
cannot point at an arbitrary directory, because the `gonative` adapter is a
Go function, not something that can be loaded dynamically from a path. The
only entrypoint registered today:

| ID | Adapter | Variants | Schedules directory |
| --- | --- | --- | --- |
| `matching-slice` | `gonative` | `vulnerable`, `fixed` | `fixtures/matching-slice/schedules` |

An unregistered entrypoint or unsupported variant is rejected with the list
of known values. Running an external fixture from the CLI is not supported
yet.

## Worker args

`scenarios.<name>.workers[].args` supplies the command parameters
(`map[string]string`) each worker runs with. The underlying engine's
`SUTConfig.Params` is scenario-wide, not per-worker, so **every worker in a
scenario must declare the same `args`** — a mismatch is rejected at load
time rather than silently using one worker's value and ignoring the rest.

```yaml
workers:
  - id: w1
    command: assign
    args:
      request_id: "42"
  - id: w2
    command: assign
    args:
      request_id: "42"   # must match w1's args exactly
```

## Timing

Four orchestrator timeouts are derived from `run.arrive_timeout_ms` as fixed
multiples: block-inference = 1×, step = 20×, run = 60×, stop = 20×. The
default `arrive_timeout_ms` (3000) is safe but slow for a scenario with a
lock-blocked path — the `matching-slice` example config sets it to `250`,
because the "fixed" variant's blocked worker waits out this exact timeout on
every run. At 3000ms, a blocked replay of 20 runs adds roughly a minute; at
250ms it adds under a second per run.

## Example

The committed example, `fixtures/matching-slice/.weavegate/config.yaml`:

```yaml
target:
  db: mysql:8.4
  schema:
    migrations: ../db/migration
    seed: ../db/seed.sql
  sut:
    adapter: gonative
    entrypoint: matching-slice
    variant: vulnerable
scenarios:
  concurrent-assign:
    workers:
      - id: w1
        command: assign
        args:
          request_id: "42"
      - id: w2
        command: assign
        args:
          request_id: "42"
    sync_points:
      - after_read_request
      - before_insert_assignment
oracle:
  assertions:
    - id: active-assignment-is-unique
      sql: |
        SELECT project_request_id, COUNT(*) AS active_assignment_count
        FROM assignment
        WHERE status = 'ACTIVE'
        GROUP BY project_request_id
        HAVING COUNT(*) > 1
      expect_rows: 0
run:
  arrive_timeout_ms: 250
```
