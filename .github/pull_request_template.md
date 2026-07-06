## Related Issue

Closes #

## Summary

- TODO

## Validation

- [ ] `git diff --check`
- [ ] Additional project-specific check:

## Review Points

- Public positioning is accurate: weavegate is an application-workflow replay gate, not a DB engine verifier, ACID verifier, general race detector, or AI verdict system.
- Deterministic replay behavior is clear when relevant: saved schedule, step trace, offending rows, report artifacts, and replay command.
- Failure and fix evidence is reproducible when relevant: same schema, same seed, same schedule, same expected result.
- Scope boundaries are explicit for runtime, CLI, Spring adapter, fixtures, reports, and GitHub Action changes.

## Scope / Non-goals

- TODO
