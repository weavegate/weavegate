# ADR 0009: Portable schedule artifact and lookup

- Status: Accepted
- Date: 2026-08-23

## Context

Exploration can discover a schedule that is not registered in the binary. The
run directory recorded that schedule only inside `scenario.json`, while the
human-readable report referred to it by content ID. That ID resolved on the
producer's machine because replay searched the producer's run evidence, but a
reader on another machine had neither that run directory nor an embedded copy.

Copying `scenario.json` did not provide a path-based workaround. It is a
run-scoped, versioned artifact that wraps the schedule with scenario metadata,
whereas `--replay <path>` deliberately accepts only the strict standalone
`{"id","steps"}` schedule contract. A portable handoff therefore needed both
a canonical file to transfer and a stable place where an unchanged replay
command could resolve its ID.

## Decision

A run with a discovered or replayed schedule writes `schedule.json` alongside
its six base artifacts. The file uses the existing canonical standalone format
and is rendered by the same schedule writer that validates its content-derived
ID. A run without a schedule omits the file instead of writing `null`.

A schedule ID resolves in three stages:

1. schedule evidence in `<out>/runs/*/scenario.json`;
2. strict standalone `*.json` files in `<out>/schedules/`;
3. schedules registered in the binary.

Resolution stops at the first stage with candidates. Keeping the portable-file
stage after run evidence prevents a user-dropped file from shadowing the run
that was just produced. Filenames in `<out>/schedules/` are not identifiers;
the verified `id` inside each file is authoritative.

The `report.md` replay line remains byte-identical and continues to omit
`--out`. The new file is additive, so `artifact_version` remains 2 under
[ADR 0007](0007-artifact-version-policy.md). `schedule.json` joins the
deterministic artifact set described by
[ADR 0005](0005-volatile-run-metadata-boundary.md).

## Consequences

- A reader can copy only `schedule.json` into `.weavegate/schedules/` and paste
  the producer's replay line unchanged.
- Scheduled runs contain seven files; runs without a selected schedule retain
  the six-file shape.
- A malformed portable schedule is an input error rather than a silently
  skipped candidate. An unresolved ID remains an input error with exit 5.
- Run evidence remains the first authority, and conflicting candidates within
  the selected stage remain ambiguous rather than being guessed at.
- Moving only the content ID is insufficient. Portability requires moving the
  schedule content that the ID names.

## What didn't work

1. **Add `--out` to the `replay:` line.** This violates ADR 0005's
   deterministic-file contract and was rejected twice in PR #33. It also does
   not help a reader on another machine, where the producer's run directory
   does not exist. The rejection is recorded here because this proposal has
   already resurfaced.
2. **Make `--replay` leniently accept `scenario.json`.** That would bind the
   run-scoped artifact contract to the portable-schedule contract and weaken
   strict unknown-field rejection, which is the standalone reader's corruption
   guard. A separate canonical file keeps both contracts narrow.
3. **Commit every discovered schedule into the fixture's `schedules/`.** Only
   schedules known at build time would then resolve, so this does not
   generalize to schedules discovered from user fixtures. A demo repository
   committing a selected file to its own `.weavegate/schedules/` is a valid,
   separate use of the portable contract.
