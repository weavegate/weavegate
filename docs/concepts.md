# Concepts

These five terms describe how weavegate controls a concurrent run, reaches a
verdict, and preserves the evidence. Each term has a corresponding public
artifact field; the artifacts are the source of truth after a run completes.

## Schedule

A **schedule** is a requested sequence of worker arrivals to release, expressed
as ordered `{worker, point}` steps. It records control intent, not a claim that
every release was realized or that the schedule violated an invariant. The
executed schedule is observable as `scenario.json.schedule.steps`, and its
portable copy is `schedule.json`; see the
[scenario and schedule fields](reference/report-schema.md#scenariojson).

## Sync-point

A **sync-point** is a named semantic boundary in the application workflow where
a worker can arrive and wait for a targeted release. Points make an execution
order controllable without using sleeps. The declared vocabulary is observable
as `scenario.json.sync_points`, while realized arrivals and releases appear in
`trace.json.events`; see the
[trace schema](reference/report-schema.md#tracejson) and
[configuration key](reference/config.md#keys).

## Oracle

An **oracle** is the component that decides whether the resulting state obeys a
declared invariant. The implemented SQL assertion oracle expects its read-only
query to return zero rows; returned rows are violations and become evidence.
The declaration is observable in `observation.json.oracles`, and failures in
`observation.json.assertion_violations` and `diagnostics`; see the
[`observation.json` fields](reference/report-schema.md#observationjson).

## Replay

A **replay** runs one saved schedule directly instead of enumerating candidates,
then repeats it to test whether the outcome is stable. It does not rediscover
the schedule. Replay is observable as `observation.json.mode` equal to
`"replay"`, `schedules_explored` equal to `0`, and a `replayed:` summary in
`report.md`; see [mode selection](reference/cli.md#mode-selection).

## Fingerprint

A **fingerprint** identifies one normalized execution outcome: complete oracle
results, ordered control trace, and normalized worker terminals, excluding
wall-clock and other volatile values. Counts for each distinct value are
observable in `observation.json.fingerprints`; exploration also records
`discovery_fingerprint` for comparison. One key with a count equal to `repeat`
means the replay outcomes agreed. See the
[fingerprint fields](reference/report-schema.md#observationjson) and the
[measured determinism example](experiments/determinism.md#repeated-result).
