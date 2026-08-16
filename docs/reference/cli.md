# CLI reference

The `weavegate` binary has two subcommands: `run` reaches a verdict on a
configured scenario; `report` prints a saved run without re-rendering it.

## `weavegate run`

```text
weavegate run --config <path> --scenario <name> [--replay <id>] [--repeat <n>] [--variant <name>] [--out <dir>]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--config` | `.weavegate/config.yaml` | Path to the run configuration file. |
| `--scenario` | (required) | Scenario name declared in `scenarios:`. Required whether or not `--replay` is set — a replay still needs the scenario's worker and sync-point declarations to execute. |
| `--replay` | (unset) | Schedule ID or file to replay instead of exploring. See [Mode selection](#mode-selection). |
| `--repeat` | config's `run.repeat` | Overrides the configured repeat count. Must be positive if given; `--repeat 0` or a negative value is rejected (exit 5) rather than silently falling back to the config value. |
| `--variant` | config's `target.sut.variant` | Overrides the configured SUT variant. |
| `--out` | `.weavegate` | Run directory base. Every run — pass or violation — writes `<out>/runs/<run_id>/`. |

### Mode selection

Whether `--replay` is present decides the mode; there is no separate mode
flag.

- **Explore mode** (`--replay` absent): sweeps the scenario's candidate
  schedules, stops at the first violation, and replays it `--repeat` times to
  check whether the violation reproduces. If every pass exhausts its
  candidates with no violation, the run passes.
- **Replay mode** (`--replay` present): runs one saved schedule `--repeat`
  times without exploring.

### `--replay` resolution order

A value containing `/` or `.json` is loaded as a schedule file directly.
Otherwise it is treated as a schedule ID (`sch_` followed by 12 hex
characters) and resolved in this order:

1. `<out>/runs/*/scenario.json` — the `violating_schedule` field of every run
   under `--out`, most recently written or not, searched in sorted path
   order.
2. The selected entrypoint's registered schedules directory (for example
   `fixtures/matching-slice/schedules/*.json`), also searched in sorted path
   order.

Resolution stops at the first stage that finds at least one match. If more
than one candidate shares the ID, their saved steps must be identical;
otherwise the ID is rejected as ambiguous (exit 5) rather than guessed at.

### `run` example

```console
$ weavegate run --config fixtures/matching-slice/.weavegate/config.yaml \
    --scenario concurrent-assign --variant vulnerable --out /tmp/wg-doc
## weavegate: FAIL
scenario: concurrent-assign | schedules explored: 1 | violating: sch_7dcb74b1e506
assertion: active-assignment-is-unique
flaky: false (repeat=20)
replay: weavegate run --config fixtures/matching-slice/.weavegate/config.yaml --scenario concurrent-assign --variant vulnerable --replay sch_7dcb74b1e506 --repeat 20
/tmp/wg-doc/runs/run_20260815T172917.706Z_bc391ac5
$ echo $?
2
```

The last stdout line before the exit is always the run directory path. The
`replay:` line is a complete command — pasting it verbatim from the same
working directory reproduces the same verdict. `--out` is deliberately
absent from it (see [`--replay` resolution order](#--replay-resolution-order)
above): replaying from a different `--out` than the one shown here falls
back to stage ②, which only resolves schedules the entrypoint has
registered. See [report-schema.md](report-schema.md) for the full
field-by-field contract.

## `weavegate report`

```text
weavegate report [run_id] [--format json|md] [--out <dir>]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `run_id` (positional) | most recent run under `--out` | Which saved run to print. |
| `--format` | `json` | `json` prints `report.json` verbatim; `md` prints `report.md` verbatim. Any other value exits 5. |
| `--out` | `.weavegate` | Run directory base to look under. |

The stored file is streamed to stdout unchanged — `report` never
re-renders. Omitting `run_id` picks the lexicographically greatest run
directory name under `<out>/runs/`; the run ID's fixed-width millisecond
timestamp (see [report-schema.md](report-schema.md)) makes lexicographic
order the same as time order, so this is unambiguous even for two runs
started in the same second. A missing run or an unknown format exits 5.

### `report` example

```console
$ weavegate report --out /tmp/wg-doc --format md
## weavegate: FAIL
scenario: concurrent-assign | schedules explored: 1 | violating: sch_7dcb74b1e506
assertion: active-assignment-is-unique
flaky: false (repeat=20)
replay: weavegate run --config fixtures/matching-slice/.weavegate/config.yaml --scenario concurrent-assign --variant vulnerable --replay sch_7dcb74b1e506 --repeat 20
```

## Global behavior

- `weavegate` with no arguments prints help and exits 0.
- An unknown subcommand or an unknown flag exits 5 with a one-line message on
  stderr; Cobra's usage dump is suppressed so the diagnostic stays short.
- Results meant for a human or a script go to stdout; diagnostics go to
  stderr.
- `weavegate --version` prints the build version and exits 0.

## Current limits

- Only the built-in `gonative` adapter is supported; `target.sut.adapter`
  accepts no other value yet.
- Only the entrypoints registered in the binary can run — currently
  `matching-slice`. An external fixture cannot be selected from the CLI (see
  [config.md](config.md)).
- `report --timeline` does not exist yet; no flag reserves that name.
