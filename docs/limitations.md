# Limitations

Weavegate makes a bounded claim: it coordinates declared sync points, repeats
the resulting database-backed workflow, and evaluates the configured Oracles.
The following boundaries describe what those results do not claim.

## A saved schedule is coordination intent

A saved schedule records the requested order of declared worker sync points. It
does not claim to record the release order realized by the database and runtime:
locking can delay a worker, a pending point can arrive later, and an intent for
a completed worker can be skipped. The exploration experiment documents this
distinction and its measured boundary in
[Matching-slice schedule exploration](experiments/exploration.md#evidence-boundary).
The baseline comparison likewise limits its result to reproducibility rather
than claiming a general detection-rate advantage; see its
[Evidence boundary](experiments/baseline-comparison.md#evidence-boundary).

## The CLI runs built-in entrypoints only

The current CLI does not load an arbitrary application workflow or external
fixture. It selects a Go adapter and entrypoint compiled into the binary;
`matching-slice` is the only registered entrypoint. The supported adapter,
entrypoint, and variant boundary is listed in the
[configuration reference](reference/config.md#built-in-entrypoints).

## Worker arguments are scenario-wide

Workers in one scenario cannot receive different argument maps. The engine
uses one scenario-wide parameter map, so every worker must declare identical
`args`; configuration loading rejects a mismatch. The constraint and accepted
shape are documented under
[Worker args](reference/config.md#worker-args).

## Exit 0 is bounded evidence, not proof of safety

Exit 0 does not claim that the workflow has no concurrency defect. Its meaning
depends on the command and mode:

- An explore-mode PASS means no configured Oracle observed a violation across
  the candidate sweeps that actually ran. Candidate bounds, declared sync
  points, configured Oracles, and the selected fixture limit that evidence.
- A replay-mode PASS means no violation was observed while repeating one saved
  schedule. Replay mode does not sweep or make a claim about other schedules.
- Exit 0 from `weavegate report` means the saved artifact was printed
  successfully. It is not a verdict; a stored FAIL report also prints with exit
  0.

See
[PASS is not a proof](reference/exit-codes.md#pass-is-not-a-proof) for the exact
exit-code contract.

## Fingerprints include timing-derived classifications

A normalized fingerprint does not claim to be independent of runtime timing.
It includes normalized trace events and worker terminal states, including
timeout-derived database-blocked classifications. Effective deadlines can
therefore change fingerprints and the resulting flaky verdict. The boundary of
what that diagnostic does not claim is described in the
[RG090 reference](reference/diagnostics/RG090.md#what-this-code-does-not-claim).

## Diagnostic codes are broad classifications

A diagnostic code does not uniquely identify a root cause. Every SQL assertion
violation currently maps to `RG001`, regardless of the assertion's more specific
domain meaning; the configured Oracle and captured rows provide that detail.
The current classification resolution and non-claims are recorded in the
[RG001 reference](reference/diagnostics/RG001.md#what-this-code-does-not-claim) and
[ADR 0006](adr/0006-diagnostic-mapping-key.md).
