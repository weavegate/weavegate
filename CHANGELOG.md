# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

`YYYY-MM-DD` in a release heading marks a section that has not been tagged yet.
Replacing it with the tag date is part of the release process, and the release
workflow fails before publication if the placeholder remains.

[Unreleased]: https://github.com/weavegate/weavegate/compare/v0.1.0-alpha...HEAD
[0.1.0-alpha]: https://github.com/weavegate/weavegate/releases/tag/v0.1.0-alpha

## [Unreleased]

## [0.1.0-alpha] - YYYY-MM-DD

### Added

- A deterministic sync-point runtime for coordinating worker execution.
- Exhaustive saved-schedule exploration and repeated replay.
- SQL assertion oracles that retain violating rows as evidence.
- `weavegate run` and `weavegate report` CLI commands.
- Six base run artifacts: manifest, scenario, observation, trace, JSON report,
  and Markdown report, plus a portable `schedule.json` when a run replays or
  discovers a schedule.
- Three-step replay lookup across saved run evidence, portable files under
  `<out>/schedules/`, and schedules built into the selected entrypoint, so an
  unchanged replay line can travel without its original run directory.
- `RG001` assertion-violation and `RG090` determinism diagnostics.
