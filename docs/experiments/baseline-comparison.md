# Baseline detection compared with a saved schedule

This experiment measures how often the matching-slice invariant is violated
under overlapping, non-overlapping, and schedule-controlled executions. The
measurement is about detection frequency and reproducibility, not throughput
or latency.

## Workload and method

All arms use request `42`, the vulnerable `concurrent-assign` workflow,
the same schema and seed, and the `active-assignment-is-unique` SQL assertion.
One MySQL container is provisioned for each test invocation and reset before
every iteration or replay run.

The hot-spot knowledge is the same in every arm:

- `plain` uses the production `NewRegistry(nil)` path and its no-op sync point.
  The harness starts both worker invocations before collecting either result.
- `hinted_delay` uses a test-only sync-point implementation that waits 2 ms at
  `before_insert_assignment`. This is not the production no-op path. The delay
  is context-aware but does not coordinate workers, impose an order, or use a
  barrier.
- `staggered_launch` invokes `w1`, waits 2, 20, or 100 ms, then invokes `w2`.
  The wait is context-aware and does not coordinate the workers. All three
  declared values are reported; none was selected after observing the result.
- `control_serial` consumes the complete `w1` result before invoking `w2`, so
  the transactions do not overlap. It uses no timing delay.
- `saved schedule` controls the order at the declared points with committed
  schedule `sch_ba00582f9632` and replays it 20 times.

After each run, the existing SQL Oracle decides whether the invariant was
violated. Neither the test nor the harness contains verdict logic. The
baseline and control arms use the one-Oracle baseline set; replay uses a
two-Oracle set. The headline assertion is the same, but `same_fixture=true`
does not mean that the Oracle sets are completely symmetric.

The vulnerable read is non-locking, the `assignment` table has no uniqueness
constraint for this invariant, and `BeginTx(ctx, nil)` leaves MySQL 8.4 at its
default `REPEATABLE READ` isolation. If both transactions establish their
snapshots before either commits, both can observe `EXISTS=false` and insert.
The measurement was made on one host, but this mechanism is not attributed to
host timing.

## Measured result

The representative values below are the first invocation in the recorded
`-count=3` run. The range is the minimum and maximum detection count across all
three invocations; counts are not rounded.

| Execution | Arm | Runs | Detected violations | `-count=3` range | Saved-command replay? |
| --- | --- | ---: | ---: | ---: | --- |
| Overlapping | Plain | 100 | 100/100 | 100-100/100 | No |
| Overlapping | Hinted delay 2 ms | 100 | 100/100 | 100-100/100 | No |
| Overlapping | Staggered launch 2 ms | 100 | 100/100 | 100-100/100 | No |
| Non-overlapping | Staggered launch 20 ms | 100 | 0/100 | 0-0/100 | No |
| Non-overlapping | Staggered launch 100 ms | 100 | 0/100 | 0-0/100 | No |
| Non-overlapping | `control_serial` | 100 | 0/100 | 0-0/100 | No |
| Controlled | Saved schedule[^replay-assertion] | 20 | 20/20 | 20-20/20 | Yes |

[^replay-assertion]: The replay helper fails the test unless all 20 runs
    violate the invariant. This row is a regression assertion, not a sampled
    detection rate; its prior evidence is recorded in
    [the determinism experiment](determinism.md).

Every measured arm completed with zero worker errors and zero MySQL 1213
deadlocks. The three conditions that still overlapped detected 300 violations
in 300 iterations. The three conditions without overlap detected none in 300
iterations. On this host, the transition occurred between the declared 2 ms
and 20 ms launch offsets. That interval estimates the offset needed to let the
first transaction finish; it is not a direct transaction-duration benchmark.

One raw comparison marker from the representative invocation is:

```text
MATCHING_BASELINE_COMPARE baseline_plain=100/100 baseline_hinted=100/100 baseline_staggered_2ms=100/100 baseline_staggered_20ms=0/100 baseline_staggered_100ms=0/100 control_serial=0/100 schedule_replay=20/20 schedule=sch_ba00582f9632 image=mysql:8.4 same_fixture=true replayable=schedule_only
```

## Timing, environment, and reproduction

Across the recorded `-count=3` run, loop timings were 7.32-7.61 s for plain,
7.65-8.51 s for hinted delay, 7.78-8.68 s for staggered 2 ms,
9.32-10.47 s for staggered 20 ms, 18.04-19.57 s for staggered 100 ms, and
6.93-7.99 s for `control_serial`. These ranges exclude container provisioning
and saved-schedule replay. They are descriptive, not benchmark results.

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
shape used for the ranges above. The baseline and control arms produce new
outcomes; only the saved-schedule arm has a schedule ID and a replayable
command.

## Evidence boundary

This experiment does not show that an ordinary integration test can never find
the defect. In this fixture, the measured discriminator was whether the two
transactions overlapped, not where a delay was placed: plain, hinted delay,
and a 2 ms stagger all detected 100/100, while 20 ms, 100 ms, and serial launch
detected 0/100. A hand-placed launch delay must exceed the transaction's
effective length to change that outcome. That threshold varies with the host
and database state and is not normally measured by the developer. A saved
schedule does not need to know the duration because it controls declared
ordering directly.

The `control_serial=0/100` result establishes overlap as a necessary condition
for this violation in the measured fixture. The overlapping arms' 300/300 is
empirical support, not proof that overlap under `REPEATABLE READ` is sufficient
in isolation. Such a claim requires an isolation-level control such as READ
COMMITTED versus REPEATABLE READ, which is outside this experiment.

The baseline arms do not leave a schedule ID, a replay command for the observed
ordering, or verdict artifacts. This experiment measures detection frequency
and reproducibility on one host only. The weavegate distinction measured here
is that the committed schedule controls and repeats the same execution and can
be used by the CLI to retain evidence; it is not a claim that only weavegate
can detect the violation. Measurements on other hosts remain future work.
