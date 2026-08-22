# Exit codes

`weavegate run` and `weavegate report` both resolve to exactly one of these
codes.

| Code | Meaning |
| --- | --- |
| `0` | For `run`, no violation; for `report`, the artifact was printed successfully. See [PASS's limit](#pass-is-not-a-proof) below. |
| `2` | An invariant violation was detected and reproduced. A SQL assertion violation is named [RG001](diagnostics/RG001.md). |
| `3` | The determinism check failed (`flaky`) and is named [RG090](diagnostics/RG090.md) — a judgment could not be trusted, not a clean pass or a clean violation. |
| `4` | Fixture provisioning, database operation, or cleanup failed. |
| `5` | A configuration, adapter, assertion, schedule, or artifact I/O error. |
| `130` | The run was interrupted by SIGINT or SIGTERM. |

`weavegate report` streams a stored artifact and does not recalculate its
verdict. Its exit 0 reports successful output, so a stored FAIL report can also
be printed with exit 0.

## Priority: error beats verdict, flaky beats violation

If the run itself failed — a fixture couldn't start, a config was invalid, an
Oracle failed to evaluate, or the operation was interrupted — that failure is
reported and no verdict is implied. Error classifications take priority in
this order: interruption → 130, fixture/database failure → 4, and input,
output, or unclassified failure → 5. A completed run's verdict is then decided
in this order:

1. `flaky` → 3
2. a violation was reproduced → 2
3. otherwise → 0

A run that is `flaky` is never reported as PASS or as a stable violation,
even when most replay runs agree.

The RG code names an already-decided verdict; it does not participate in this
priority calculation. Adding RG001 leaves a violation at exit 2, and adding
RG090 leaves a flaky run at exit 3.

## The `flaky` determination

`flaky` reuses the orchestrator's existing replay contract; it does not
invent a new predicate:

- **Replay mode** (`--replay` given directly): `flaky := replay.Flaky` — the
  replayed schedule did not produce the same fingerprint on every one of the
  `--repeat` runs.
- **Explore mode**: `flaky := replay.Flaky` **or** the replay's single
  fingerprint differs from the fingerprint of the run exploration first
  found violating.

The second clause in explore mode exists specifically to catch the
0-of-`repeat` case: exploration finds a violation once, but every one of the
20 replay runs individually agrees with every other individual run —
`replay.Flaky` is `false` — while none of them actually reproduce the
violation. Without the fingerprint comparison this would leak out as exit 0.
With it, a discovery that cannot be reproduced on replay is always reported
as a determinism failure (exit 3), never a pass.

## PASS is not a proof

In explore mode, exit 0 means: **no violation was observed across
`run.explore_passes` full sweeps of the candidate schedule set.** It is not a
proof that no
interleaving of this scenario can violate the invariant — a database's
realized release order under contention, retry, and timing can diverge from
the saved candidate space (see [report-schema.md](report-schema.md) for what
a saved schedule does and does not capture). Raising `run.explore_passes`
narrows this gap; it cannot close it.

In replay mode, exit 0 is limited to the one schedule repeated by that command.
Replay mode does not sweep or make a claim about any other schedule.

## Cleanup failures never mask a verdict

A teardown (container/fixture cleanup) failure follows one rule: **it never
lowers an already-decided code, and it only raises a passing run.**
Concretely:

- Verdict was 2, 3, or already 4/5: the code is unchanged; the cleanup
  failure is a warning on stderr only.
- Verdict was 0 (PASS): the code becomes 4.

The scenario's own outcome — the verdict itself, and everything in
`scenario.json`/`observation.json`/`trace.json`/`report.md` — is decided
before teardown runs and is never changed by a cleanup failure: whether
teardown happens to succeed is a fact about this run's environment, not
about the scenario, and those four files stay byte-identical for identical
config regardless of it (see
[report-schema.md](report-schema.md#volatile-vs-deterministic)). Teardown
itself runs, and the exit code above is decided, before the report is
written and printed — not only in a deferred cleanup step afterward — so
`cleanup_failed` in the saved `manifest.json` and the process's actual exit
code are never one run's snapshot mismatched against the other's.

This keeps a leaked container from being silently reported as PASS, while
never letting a teardown problem hide a real violation.

### Bounded cleanup after interruption

SIGINT and SIGTERM cancel the operation context. Once a fixture has been
created, weavegate requests `Teardown` exactly once with operation cancellation
detached and an independent 30-second deadline. A deadline or teardown error
is printed as a warning; for a completed run it is also stored as
`manifest.cleanup_failed` and follows the exit-code rule above.
On the first signal, weavegate restores the default signal disposition before
canceling the operation; a repeated signal therefore terminates the process
even while detached cleanup is still running.
Each application or administrative pool close receives at most five seconds
of that budget, leaving the remainder for terminating the external container.

This is a bounded cleanup request, not a guarantee that an uncooperative
external library will return or that SIGKILL can be handled. No in-process
cleanup can run after SIGKILL.

## Exit 5's scope

Exit 5 covers every input- and output-shaped failure, not just configuration
syntax:

- an invalid or ambiguous `config.yaml` (see [config.md](config.md))
- an unsupported adapter, entrypoint, or variant
- a malformed or duplicate assertion
- an unresolvable or ambiguous `--replay` schedule ID (see [cli.md](cli.md))
- a run-directory write failure — permission, disk, or rename — writing the
  six run artifacts (see [report-schema.md](report-schema.md))
- an existing destination run directory or a short stdout write from `run`
  or `report`

An unclassified internal error also resolves to 5 rather than silently
succeeding.
