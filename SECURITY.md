# Security policy

## Supported versions

weavegate has not published a release yet. Security fixes land on the `main`
branch only.

| Version | Supported |
| --- | --- |
| `main` | Yes |
| Tagged releases | None published yet |

## Reporting a vulnerability

Email `jaeunda@gmail.com` with the details. Please do not open a public issue
or a discussion thread for a security report.

Include what you can:

- the commit SHA you tested against,
- the steps needed to reproduce the behavior,
- what an attacker gains from it,
- any log, trace, or report output that shows it.

## Response expectations

weavegate is maintained by one person. Expect an acknowledgement within seven
days. If a report is accepted, the fix and the disclosure timing are agreed
with the reporter before the fix is published. If a report is declined, you
will get the reasoning.

## Scope

weavegate is a testing tool that runs workflows against disposable databases.
The following are in scope:

- sync-point instrumentation that can be reached or activated outside a test
  build,
- fixture, schedule, or scenario input that causes unintended commands to run
  against a database or the host,
- credentials, connection strings, or database contents leaking into reports,
  traces, or logs.

## Out of scope

Vulnerabilities in MySQL, InnoDB, Docker, Testcontainers, or any other
upstream dependency belong to those projects; please report them there.

A failing oracle, a reproduced application race, or a schedule that breaks the
workflow under test is the tool doing its job, not a vulnerability in
weavegate.
