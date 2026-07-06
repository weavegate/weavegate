---
name: github-workflow
description: "Manage weavegate's public open-source GitHub delivery workflow end to end: inspect local state, create a GitHub issue, create an issue-numbered branch, run repository-appropriate validation, make a conventional commit, push, and open a pull request. Use when the user asks to publish, wrap up, ship, create an issue/branch/PR, rename a branch, or prepare OSS-ready GitHub artifacts for weavegate changes."
---

# GitHub Workflow

## Overview

Use this skill to turn a weavegate change into public, reviewable GitHub artifacts. Keep the workflow non-interactive where possible, preserve unrelated local changes, and make every issue, commit, and PR understandable to outside contributors.

weavegate is intended to become an open-source developer tool. Treat GitHub artifacts as part of the product surface: clear scope, reproducible validation, honest limitations, and traceable issue-to-branch-to-PR links matter.

## Preconditions

1. Inspect local state before doing remote work:
   - `git status --short --branch`
   - `git remote -v`
   - `git log --oneline -5`
2. Do not revert, restage, or clean unrelated user changes. Stage only the paths that belong to the requested change.
3. Check GitHub CLI authentication before creating remote artifacts:
   - `gh auth status`
4. If GitHub auth, network access, or permissions block the workflow, complete the local steps that are safe, then report the exact blocker.
5. Run `gh` commands non-interactively. In restricted environments, request the required approval instead of trying to bypass network limits.

## Repository Context

Use the repository's current implementation stage to choose validation:

- Early planning state: markdown, `_workbench/` planning documents, `docs/`, `.agent/skills/`, and repository metadata.
- MVP/Phase 1 implementation state: Go CLI, Testcontainers MySQL fixtures, Spring test-slice adapter, GitHub Action, generated reports/traces, and OSS docs.
- Public positioning: weavegate is a deterministic CI replay gate for schedule-dependent database bugs in Spring Boot + MySQL/InnoDB workflows. Do not market it as a general race detector, DB engine verifier, ACID verifier, or AI verdict system.

## Required Validation

Always run:

- `git diff --check`

Then select validation based on changed files and available project files:

- Markdown, docs, skills, templates:
  - Review manually for broken links, stale phase labels, overstated claims, and public-facing clarity.
  - For `SKILL.md`, validate skill format when the validator is available:
    `python3 /home/daeun/.codex/skills/.system/skill-creator/scripts/quick_validate.py <skill-dir>`
- Go code:
  - `go test ./...`
  - `go test -race ./...` when the change touches concurrency, sync-point runtime, orchestration, adapters, or fixture execution.
  - `gofmt`/`go test` should not rewrite unrelated files.
- Testcontainers, MySQL, fixtures, or CI demo behavior:
  - Prefer the narrow fixture/demo command documented by the repo once it exists.
  - If Docker or Actions parity is required but unavailable locally, state the environment limitation explicitly.
- Java/Spring adapter:
  - Run the adapter's native build/test command once it exists, such as `./gradlew test` or `mvn test`, scoped to the adapter if possible.
- GitHub Action or workflow files:
  - Check YAML syntax if tooling is available.
  - Confirm referenced action paths, artifact names, permissions, and expected exit codes.
- Release or distribution files:
  - Run dry-run validation if supported, such as a goreleaser check/dry run, without publishing.

Report pass/fail/skip status in the final handoff and include the same validation list in the PR body. If a validation command is skipped, give the reason.

## Workflow

1. Summarize the change in one line and choose the conventional commit type.
   - Prefer `feat`, `fix`, `docs`, `test`, `refactor`, `ci`, `build`, or `chore`.
   - Useful scopes: `cli`, `syncpoint`, `orchestrator`, `oracle`, `diagnostic`, `report`, `fixture`, `spring`, `action`, `docs`, `skills`, `workbench`, `release`.
2. Create or identify the GitHub issue.
   - Create an issue for the full workflow unless the user provided an existing issue.
   - Use the eventual PR/commit title style, for example `docs(skills): add weavegate GitHub workflow`.
   - Include `Summary`, `Validation`, and, when relevant, `Scope / Non-goals`.
   - When creating issues with `gh issue create --body`, write the body in the same structure as `.github/ISSUE_TEMPLATE/issue.md`; the CLI does not merge the repository template into an explicitly supplied body.
3. Create the branch from the intended base.
   - Base is usually `main` unless repository context says otherwise.
   - Branch format: `<type><issue-number>/<short-kebab-summary>`.
   - Examples: `docs42/add-github-workflow-skill`, `feat108/syncpoint-runtime`, `ci151/action-artifacts`.
4. Run validation before committing when practical. If the change requires committing before remote CI can run, run local validation first and mention remaining CI validation in the PR.
5. Stage only intended files:
   - `git add <paths...>`
6. Commit with one conventional message:
   - `<type>(<scope>): <summary>`
7. Push the branch:
   - `git push -u origin <branch-name>`
8. Open the PR.
   - Title should match the issue and commit intent.
   - Body must follow the same structure as `.github/pull_request_template.md`; the CLI does not merge the repository template into an explicitly supplied body.
   - Body must include `Related Issue`, `Summary`, `Validation`, `Review Points`, and `Scope / Non-goals`.
   - Put `Closes #<issue-number>` under `Related Issue`.

## Issue and PR Body Style

Use concise public-facing English. Avoid private planning shorthand unless the PR is explicitly limited to `_workbench/`.

Template:

```markdown
## Related Issue

Closes #123

## Summary
- ...
- ...

## Validation
- PASS: git diff --check
- PASS: go test ./...
- SKIP: go test -race ./... (docs-only change)

## Review Points
- Public positioning is accurate when relevant.
- Deterministic replay behavior is clear when relevant.
- Failure and fix evidence is reproducible when relevant.
- Scope boundaries are explicit.

## Scope / Non-goals
- ...
```

For public OSS PRs, prefer outcome-oriented bullets over implementation diary. Mention deterministic replay evidence, fixture names, exit codes, and artifacts when those are part of the change.

## Naming Guidance

- Issue, PR, and commit titles should be consistent and conventional:
  - `feat(syncpoint): add arrive and release runtime`
  - `fix(orchestrator): handle blocked workers during replay`
  - `docs(related-work): clarify weavegate scope`
  - `ci(action): upload replay artifacts`
- Branch names must include the issue number directly after the type prefix:
  - `feat17/syncpoint-runtime`
  - `fix28/blocked-worker-timeout`
  - `docs42/github-workflow-skill`
- Keep names lowercase, kebab-case, and short enough to read in PR lists.

## Command Patterns

Use non-interactive commands:

```bash
gh issue create --repo <owner/repo> --title "<title>" --body "<body>"
gh pr create --repo <owner/repo> --base main --head <branch> --title "<title>" --body "<body>"
```

When capturing an issue number:

```bash
issue_url="$(gh issue create --repo <owner/repo> --title "<title>" --body "<body>")"
issue_number="${issue_url##*/}"
```

Quote multi-line bodies carefully. Avoid command substitutions or backticks inside double-quoted body strings unless they are intentionally escaped.

## Branch Rename Notes

When the user explicitly asks to rename a branch:

1. Rename locally with `git branch -m <new-branch-name>`.
2. Push the renamed branch with `git push -u origin <new-branch-name>`.
3. Do not delete the old remote branch unless the user explicitly asks.

## Final Handoff

Return:

- issue number and URL, if created
- branch name
- commit SHA and message, if committed
- PR number and URL, if created
- validation results

If any step failed, separate completed local work from blocked remote work and include the exact reason.
