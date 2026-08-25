# ADR 0008: Diagnostic evidence model

- Status: Accepted
- Date: 2026-08-19

## Context

Diagnostic `evidence` began with `schedule_ref`, `rows`, and `trace`, then
gained `observation` and `evidence_sets`. The schema described the fields but
did not state what the evidence object itself meant. Two interpretations were
therefore mixed: a list of every artifact that supports a diagnostic, or one
pointer telling a reader where to look next.

That ambiguity produced three consecutive review findings in the same field.
WG090 first pointed at a trace that could not demonstrate fingerprint
divergence. Assertion diagnostics then all pointed at one trace, including
assertions that passed in the saved run. Restricting the trace to assertions
violated in that run fixed the false pointer but removed `observation.json`,
the only artifact that preserves the violating rows rendered by WG001.

## Decision

Diagnostic evidence is inclusive: it names every saved artifact that supports
the diagnostic.

- `schedule_ref` identifies the executed schedule; it is not an artifact
  pointer.
- An assertion diagnostic always names `observation.json`, because its
  violating rows exist in `assertion_violations` there.
- It additionally names `trace.json` only when the saved trace comes from a
  run where that assertion was violated.
- Engine-derived WG090 names only `observation.json`, because a single trace
  cannot demonstrate fingerprint divergence.

The named `trace` and `observation` fields remain in the schema. Replacing them
with a generic `artifacts` array would make the inclusive shape more apparent,
but named fields are easier for consumers and the missing piece was the rule,
not the representation.

## Consequences

- One diagnostic can normally contain more than one artifact pointer.
- Each pointer has a distinct role: `observation.json` preserves row or
  fingerprint evidence, while a corresponding `trace.json` preserves execution
  order and terminal state.
- When a future artifact supports a diagnostic, extending evidence means adding
  that true pointer rather than selecting it in place of another true pointer.
- The WG001 demonstration now renders both `trace.json` and
  `observation.json`; WG090 remains observation-only.

## What didn't work

Treating `trace` and `observation` as exclusive alternatives did not work. The
attempt corrected a false trace pointer by choosing one next place to inspect,
but it also discarded the true observation pointer needed to verify the
rendered row evidence.

The deeper mistake was overlooking that `schedule_ref` and `trace` had
coexisted from the first evidence shape. Evidence was already an inclusive
description, not a single navigation target. Making two later artifact fields
exclusive introduced a model the surrounding object had never followed.
