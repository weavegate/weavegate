---
name: Fixture contribution
about: Propose or contribute a new fixture.
title: "Fixture: "
labels: "enhancement"
assignees: "jaeunda"
---

## Summary

- Describe the domain and the invariant the fixture is meant to verify.

## Files

- [ ] `fixtures/<name>/README.md` — the domain and the invariant it verifies
- [ ] `fixtures/<name>/db/migration/*.sql` — synthetic schema
- [ ] `fixtures/<name>/db/seed.sql` — seed data
- [ ] `fixtures/<name>/schedules/*.json` — saved coordination schedules
- [ ] `fixtures/<name>/sut/` — the Go-native system under test, vulnerable and fixed paths
- [ ] `fixtures/<name>/*_test.go` — fixture lifecycle test (schema, seed, reset)
- [ ] `fixtures/<name>/sut/*_test.go` — the oracle declaration and reproduction evidence

## Validation

- If implemented, describe the command used to reproduce the fixture's behavior and the evidence it produces. If this is a proposal, describe the command and evidence you plan to provide instead.

## Notes

- Mention only work that will be implemented in a follow-up, or remove this section.
