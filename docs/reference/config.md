# Configuration reference

`weavegate run --config <path>` reads exactly one YAML document. Decoding is
strict: an unrecognized key — including a typo — or a trailing second YAML
document is rejected rather than silently ignored.

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

Diagnostic codes are not declared by configuration. Weavegate derives them
from the Oracle kind or engine signal that produced the verdict and loads their
text from its embedded rule table. In particular, adding a `diagnostic:` key to
an assertion or anywhere else is rejected by strict decoding. See the
[RG001 reference](diagnostics/RG001.md) for the current assertion mapping.

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
every run.

Measure the effect on the same replay rather than inferring it from one run.
The following commands time 20 repeats with the committed 250ms value, then
with a temporary config beside the original whose relative fixture paths stay
valid:

```bash
CFG=fixtures/matching-slice/.weavegate/config.yaml
FAST_OUT=$(mktemp -d)
/usr/bin/time -f 'arrive=250ms elapsed=%e seconds' \
  weavegate run --config "$CFG" --scenario concurrent-assign \
  --variant fixed --replay sch_ba00582f9632 --repeat 20 --out "$FAST_OUT"
test $? -eq 0

SLOW_CFG=$(mktemp fixtures/matching-slice/.weavegate/timing.XXXXXX.yaml)
trap 'rm -f "$SLOW_CFG"' EXIT
sed 's/arrive_timeout_ms: 250/arrive_timeout_ms: 3000/' "$CFG" > "$SLOW_CFG"
SLOW_OUT=$(mktemp -d)
/usr/bin/time -f 'arrive=3000ms elapsed=%e seconds' \
  weavegate run --config "$SLOW_CFG" --scenario concurrent-assign \
  --variant fixed --replay sch_ba00582f9632 --repeat 20 --out "$SLOW_OUT"
test $? -eq 0
```

Wall-clock results depend on the host; record both emitted timing lines when
making a quantitative comparison.

## Exploration candidate limit

The built-in exhaustive strategy counts the complete candidate space before
the fixture is created. The CLI accepts at most 5,000 candidates. A larger
scenario exits 5 during preflight instead of starting a container or lazily
discovering the excess after partial execution. The limit is not currently a
config key or CLI flag.

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
