# ADR 0012: Preserve evidence when diagnostic derivation fails

- Status: Accepted
- Date: 2026-09-10
- Issue: [#44](https://github.com/weavegate/weavegate/issues/44)

## Context

The CLI decides a scenario verdict during execution, then tears down the
fixture, derives diagnostics, and writes the run directory. Diagnostic
derivation names an already-decided verdict; it does not decide whether an
Oracle passed or failed. Nevertheless, a derivation error previously returned
exit 5 before `report.WriteRun`, discarding the schedule, assertion rows,
normalized trace, fingerprints, and replay command from a completed run.

This can only be an internal invariant failure with the current preflighted
diagnostic table, such as an Oracle violation kind without a mapping. Its rarity
does not make the evidence loss safe. At the same time, treating the run's
semantic verdict as the process outcome would conceal that weavegate failed to
produce its promised diagnostic presentation.

## Decision

Choose policy 2: when diagnostic derivation fails after execution, write and
print the report without diagnostics, then return the derivation failure as a
`ci.OutputError` (exit 5).

- Derivation runs once, after execution and teardown. Any partial diagnostic
  result returned with an error is discarded.
- The CLI writes the normal atomic six- or seven-file run directory with an
  empty `diagnostics` array. Oracle declarations, assertion violations,
  schedule, trace, fingerprints, replay command, and the semantic FAIL, FLAKY,
  or PASS headline remain unchanged.
- After `report.md` and the run-directory path are printed to stdout, the
  derivation error is printed to stderr and wins over the semantic verdict.
  Artifact write or stdout failures still take precedence at the point where
  they occur.
- `ci.OutputError` covers failures to produce the artifact presentation as well
  as artifact I/O. No new exit code is added: exit 5 already means the command
  did not complete its output contract.

For a normal verdict exit, `diagnostics: []` means no diagnostic applied. For an
exit 5 caused by derivation failure, it means the run evidence was deliberately
preserved but no diagnostic was successfully produced. The live process exit
and stderr carry that distinction; the saved JSON does not persist the internal
error text.

## Consequences

- A completed Oracle evaluation is not discarded because its diagnostic
  presentation failed.
- The run directory remains an all-at-once atomic publication rather than a
  partially visible directory that is mutated after publication.
- Deterministic artifacts remain functions of execution evidence. They do not
  embed potentially volatile internal error text.
- A diagnostic-free FAIL or FLAKY report is valid retained evidence, but exit 5
  tells automation not to treat it as a completed diagnostic result.
- A later `weavegate report` reads the stored artifact and exits according to
  that command's output contract; it does not reconstruct the original
  derivation error.

## What didn't work

1. **Write core artifacts first and merge diagnostics afterward.** Publishing
   twice would either expose a partial run directory or require in-place
   replacement of files, weakening the existing all-files-at-once atomic write
   contract. A hidden staging directory would still reduce to the selected
   policy when derivation failed.
2. **Abort without artifacts.** This kept the implementation simple but made an
   output-layer invariant failure erase already-completed Oracle and schedule
   evidence, contrary to the product's evidence-oriented purpose.
3. **Return the semantic verdict after saving.** Exit 0, 2, or 3 would claim the
   command completed its output contract even though diagnostic production
   failed. Error priority requires exit 5 instead.
4. **Persist the derivation error in deterministic artifacts.** Internal error
   text is not execution evidence and can change independently of an identical
   schedule and verdict. Stderr is the appropriate channel for that cause.
