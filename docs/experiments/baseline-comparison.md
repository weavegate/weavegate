# Baseline detection compared with a saved schedule

This experiment measures how often the matching-slice invariant is violated
when the vulnerable workflow runs concurrently without ordering control. It
compares two uncontrolled baseline arms with the existing saved schedule. The
measurement is about detection frequency and reproducibility, not throughput
or latency.

## Workload and method

All three arms use request `42`, the vulnerable `concurrent-assign` workflow,
the same schema and seed, and the `active-assignment-is-unique` SQL assertion.
One MySQL container is provisioned for each test invocation and reset before
every iteration or replay run.

The hot-spot knowledge is the same in all three arms:

- `plain` uses the production `NewRegistry(nil)` path and its no-op sync point.
  The harness starts both worker invocations before collecting either result.
- `hinted_delay` uses a test-only sync-point implementation that waits 2 ms at
  `before_insert_assignment`. This is not the production no-op path. The delay
  is context-aware but does not coordinate workers, impose an order, or use a
  barrier.
- `saved schedule` controls the order at the declared points with committed
  schedule `sch_ba00582f9632` and replays it 20 times.

After each run, the existing SQL Oracle decides whether the invariant was
violated. Neither the test nor the harness contains verdict logic.

## Measured result

The representative values below are the first invocation in the recorded
`-count=3` run. The range is the minimum and maximum detection count across all
three invocations; counts are not rounded.

| Arm | Runs | Detected violations | `-count=3` range | Re-creatable by a saved command? |
| --- | ---: | ---: | ---: | --- |
| Plain | 100 | 100/100 | 100-100/100 | No |
| Hinted delay 2 ms | 100 | 100/100 | 100-100/100 | No |
| Saved schedule | 20 | 20/20 | 20-20/20 | Yes |

Every measured arm completed with zero worker errors and zero MySQL 1213
deadlocks. The plain result shows that this workload was readily detected on
this host; the saved schedule does not claim exclusive ability to detect it.

One raw comparison marker from the representative invocation is:

```text
MATCHING_BASELINE_COMPARE baseline_plain=100/100 baseline_hinted=100/100 schedule_replay=20/20 schedule=sch_ba00582f9632 image=mysql:8.4 same_fixture=true replayable=schedule_only
```

## Timing, environment, and reproduction

The representative baseline loops, excluding container provisioning and the
saved-schedule replay, took `8.09687758s` for plain and `8.423677429s` for the
hinted delay. Timing is descriptive and is not a benchmark result.

This measurement was recorded on:

- Linux `6.18.33.2-microsoft-standard-WSL2`, x86-64;
- Intel Core Ultra 5 125H, 18 logical CPUs;
- Docker server `29.6.1` with 7.55 GiB available memory;
- MySQL image `mysql:8.4`, version `8.4.10`, image digest
  `sha256:8dbcf531a03aade657e181b9cf2f1d1803ce621a1d55610cb44cb531ab7d7db6`;
  and
- Go `1.25.0`.

Run the complete comparison with Docker available:

```bash
go test ./fixtures/matching-slice/sut/ \
  -run '^TestBaselineComparison$' -v -count=1
```

Use `-count=3` instead of `-count=1` to reproduce the repeated measurement
shape used for the range above. The baseline arms produce new race outcomes;
only the saved-schedule arm has a schedule ID and a replayable command.

## Evidence boundary

This experiment does not show that an ordinary integration test can never find
the defect. On this host, both plain concurrency and the hand-placed delay
detected it in every measured iteration. The hinted-delay result is reported as
observed, but it depends on a person choosing the correct test seam and delay;
placing a delay at the wrong point can silently produce zero detections.

The baseline arms do not leave a schedule ID, a replay command for the observed
ordering, or verdict artifacts. Their outcomes can change with host and
database timing. This experiment measures detection frequency and
reproducibility on one host only. The weavegate distinction measured here is
that the committed schedule controls and repeats the same execution and can be
used by the CLI to retain evidence; it is not a claim that only weavegate can
detect the violation. Measurements on other hosts remain future work.
