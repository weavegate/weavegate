# Exit codes

`weavegate run` and `weavegate report` both resolve to exactly one of these
codes.

| Code | Meaning |
| --- | --- |
| `0` | No violation. See [PASS's limit](#pass-is-not-a-proof) below. |
| `2` | An invariant violation was detected and reproduced. |
| `3` | The determinism check failed (`flaky`) — a judgment could not be trusted, not a clean pass or a clean violation. |
| `4` | The fixture or database container failed to start. |
| `5` | A configuration, adapter, assertion, schedule, or artifact I/O error. |

## Priority: error beats verdict, flaky beats violation

If the run itself failed — a fixture couldn't start, a config was invalid, an
Oracle failed to evaluate — that failure is reported and no verdict is
implied. A completed run's verdict is then decided in this order:

1. `flaky` → 3
2. a violation was reproduced → 2
3. otherwise → 0

A run that is `flaky` is never reported as PASS or as a stable violation,
even when most replay runs agree.

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

Exit 0 means: **no violation was observed across `run.explore_passes` full
sweeps of the candidate schedule set.** It is not a proof that no
interleaving of this scenario can violate the invariant — a database's
realized release order under contention, retry, and timing can diverge from
the saved candidate space (see [report-schema.md](report-schema.md) for what
a saved schedule does and does not capture). Raising `run.explore_passes`
narrows this gap; it cannot close it.

## Cleanup failures never mask a verdict

A teardown (container/fixture cleanup) failure is reported after the verdict
and evidence are already final, and it follows one rule: **it never lowers
an already-decided code, and it only raises a passing run.** Concretely:

- Verdict was 2, 3, or already 4/5: the code is unchanged; the cleanup
  failure is a warning on stderr only.
- Verdict was 0 (PASS): the code becomes 4.

This keeps a leaked container from being silently reported as PASS, while
never letting a teardown problem hide a real violation.

## Exit 5's scope

Exit 5 covers every input- and output-shaped failure, not just configuration
syntax:

- an invalid or ambiguous `config.yaml` (see [config.md](config.md))
- an unsupported adapter, entrypoint, or variant
- a malformed or duplicate assertion
- an unresolvable or ambiguous `--replay` schedule ID (see [cli.md](cli.md))
- a run-directory write failure — permission, disk, or rename — writing the
  six run artifacts (see [report-schema.md](report-schema.md))

An unclassified internal error also resolves to 5 rather than silently
succeeding.
