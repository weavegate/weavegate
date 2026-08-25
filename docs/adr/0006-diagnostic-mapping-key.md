# ADR 0006: Diagnostic mapping key

- Status: Accepted
- Date: 2026-08-17

## Context

Once an Oracle has found a violation, weavegate needs to select the WG code
that names it. There were two possible owners for that selection: a user could
write a code beside each assertion in configuration, or weavegate could derive
the code from the kind of Oracle signal that fired.

The distinction is architectural. A configured code is a user-provided label;
a derived code is part of the tool's own verdict vocabulary. It also determines
whether adding diagnostics widens the configuration contract and requires
existing fixtures to opt in.

## Decision

Diagnostic codes are derived from `oracle.Violation.Kind`. The diagnostic
layer translates that kind to a closed trigger vocabulary, then looks up the
rule that owns the trigger. Each rule owns its code-scoped title, invariant,
reason, help, and one or more triggers as data in `rules/`.

Configuration does not select or override the code. A rule-table typo is a
build defect, and an unknown runtime violation kind is an error rather than an
implicit fallback classification.

## Consequences

- The configuration surface does not grow, and an existing fixture receives
  WG001 without changing its committed config.
- Adding rule text or another mapping remains a rule-table operation rather
  than an edit to every fixture.
- Every SQL assertion violation currently maps to WG001, even if the assertion
  is detecting a more specific anomaly such as a lost update.
- More specific codes arrive with an Oracle, such as a differential Oracle,
  that has enough structural knowledge to distinguish those anomaly classes.
- The dependency is one-way: the diagnostic package reads `Violation.Kind`;
  the Oracle and engine packages do not know about WG codes.
- A completed flaky verdict remains exit 3 even if its detailed fingerprint
  comparison is unavailable or internally inconsistent. WG090 then uses a
  neutral observed statement instead of turning diagnostic rendering into an
  exit 5 error; a diagnostic names the verdict and does not decide it.

## What didn't work

Putting `diagnostic: WG001` beside an assertion in config was considered and
rejected. It would turn a tool-owned judgment into a user-applied label, weaken
the compiler-diagnostic analogy, and make the extension procedure "add a rule
and edit every fixture config." It would also duplicate the existing validated
`Violation.Kind` classification instead of using the signal the firing Oracle
already provides.
