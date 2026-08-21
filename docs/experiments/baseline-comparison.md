# Baseline detection compared with a saved schedule

This experiment measures how often the matching-slice invariant is violated
under concurrent, staggered, serial, and schedule-controlled executions. The
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
The mechanism is not attributed to host timing. The counts below are bounded
to the environment and invocation that produced them.

## Measured result

The development-host measurement is one `-count=3` command. Each result below
is one invocation on that host; these columns are not a cross-host range.

| Launch | Arm | Invocation 1 | Invocation 2 | Invocation 3 | Saved-command replay? |
| --- | --- | ---: | ---: | ---: | --- |
| Concurrent | Plain | 100/100 | 100/100 | 100/100 | No |
| Concurrent | Hinted delay 2 ms | 100/100 | 100/100 | 100/100 | No |
| Staggered | Staggered launch 2 ms | 100/100 | 100/100 | 100/100 | No |
| Staggered | Staggered launch 20 ms | 0/100 | 0/100 | 0/100 | No |
| Staggered | Staggered launch 100 ms | 0/100 | 0/100 | 0/100 | No |
| Serial | `control_serial` | 0/100 | 0/100 | 0/100 | No |
| Schedule-controlled | Saved schedule[^replay-assertion] | 20/20 | 20/20 | 20/20 | Yes |

The GitHub-hosted-runner measurements are independent `-count=1` smoke
executions. Run `32457121871` recorded the runner environment shown below;
the other four runs are earlier observations from the same `ubuntu-latest`
workflow configuration, for which detailed host facts were not recorded.
Counts are newest first and are neither rounded nor replaced by a range.

| Arm | `32457121871` | `32456259979` (`main`) | `32455314019` | `32454451061` | `32453078867` |
| --- | ---: | ---: | ---: | ---: | ---: |
| Plain | 100/100 | 100/100 | 98/100 | 100/100 | 99/100 |
| Hinted delay 2 ms | 100/100 | 100/100 | 100/100 | 100/100 | 100/100 |
| Staggered launch 2 ms | 35/100 | 62/100 | 36/100 | 52/100 | 27/100 |
| Staggered launch 20 ms | 0/100 | 0/100 | 0/100 | 1/100 | 1/100 |
| Staggered launch 100 ms | 0/100 | 0/100 | 0/100 | 0/100 | 0/100 |
| `control_serial` | 0/100 | 0/100 | 0/100 | 0/100 | 0/100 |
| Saved schedule[^replay-assertion] | 20/20 | 20/20 | 20/20 | 20/20 | 20/20 |

[^replay-assertion]: The replay helper fails the test unless all 20 runs
    violate the invariant. This row is a regression assertion, not a sampled
    detection rate; its prior evidence is recorded in
    [the determinism experiment](determinism.md).

Every measured arm completed with zero worker errors and zero MySQL 1213
deadlocks. The test fails if any arm records either condition, so the tables
include only measurements without worker errors or deadlocks.

On the development host, each of the three invocations detected 300 violations
in the 300 iterations across the two concurrent arms and the 2 ms stagger. In
each invocation on that host, the 20 ms and 100 ms staggers detected 0/200 and
the serial control detected 0/100. The detection transition on that host was
therefore between the declared 2 ms and 20 ms launch offsets in all three
invocations.

On recorded runner `32457121871`, the two concurrent arms detected 200/200,
the 2 ms stagger detected 35/100, the 20 ms and 100 ms staggers detected 0/200,
and the serial control detected 0/100. Across all eight tabled invocations, the
2 ms stagger detected more violations than the 20 ms stagger. This supports the
direction of a detection transition between the two declared offsets in every
invocation. What did not reproduce was the transition's sharpness or a specific
threshold: the development host changed from 100/100 to 0/100, while the runner
changed from 27-62/100 to 0-1/100, with 2 ms already in its partial-detection
region. Neither environment directly measured a threshold. The harness does
not observe the first transaction's commit relative to the second transaction's
start, so launch-offset results are not a direct transaction-duration benchmark.

The test emits comparison-marker payloads such as these, shown without the
`go test -v` source-location prefix. The first came from development-host
invocation 1; the second came from runner `32457121871`:

```text
MATCHING_BASELINE_COMPARE baseline_plain=100/100 baseline_hinted=100/100 baseline_staggered_2ms=100/100 baseline_staggered_20ms=0/100 baseline_staggered_100ms=0/100 control_serial=0/100 schedule_replay=20/20 schedule=sch_ba00582f9632 image=mysql:8.4 same_fixture=true replayable=schedule_only
MATCHING_BASELINE_COMPARE baseline_plain=100/100 baseline_hinted=100/100 baseline_staggered_2ms=35/100 baseline_staggered_20ms=0/100 baseline_staggered_100ms=0/100 control_serial=0/100 schedule_replay=20/20 schedule=sch_ba00582f9632 image=mysql:8.4 same_fixture=true replayable=schedule_only
```

## Timing, environment, and reproduction

Across the recorded `-count=3` run, loop timings were 7.32-7.61 s for plain,
7.65-8.51 s for hinted delay, 7.78-8.68 s for staggered 2 ms,
9.32-10.47 s for staggered 20 ms, 18.04-19.57 s for staggered 100 ms, and
6.93-7.99 s for `control_serial`. These ranges exclude container provisioning
and saved-schedule replay. They are descriptive, not benchmark results.

The development-host `-count=3` measurement was recorded on:

- Linux `6.18.33.2-microsoft-standard-WSL2`, x86-64;
- Intel Core Ultra 5 125H, 18 logical CPUs;
- Docker server `29.6.1` with 7.55 GiB available memory;
- MySQL image `mysql:8.4`, version `8.4.10`, image digest
  `sha256:8dbcf531a03aade657e181b9cf2f1d1803ce621a1d55610cb44cb531ab7d7db6`;
  and
- Go `1.25.0`.

GitHub Actions run `32457121871`, at commit `46fd73b`, recorded its
`ubuntu-latest` runner as:

- Linux `6.17.0-1022-azure`, x86-64;
- AMD EPYC 9V74 80-Core Processor, 2 logical CPUs available to the runner;
- `8128880 kB` total memory;
- GitHub runner image OS `ubuntu24`, image version `20260816.277.1`;
- Docker server `28.0.4`;
- MySQL image `mysql:8.4`, repository digest
  `sha256:b3b90af2a6552ae30c266fdb7d5dd55f3afb72404bb78d37fe8a23eb857fd3fb`;
  and
- Go `1.25.0`.

The runner log did not record the MySQL patch version, so none is inferred
from the tag or digest. The environment facts above apply only to run
`32457121871`; the earlier four runner logs record the workflow configuration
and comparison marker, but not the allocated hardware, runner image version,
Docker version, or image digest.

Run the complete comparison with Docker available:

```bash
go test ./fixtures/matching-slice/sut/ \
  -run '^TestBaselineComparison$' -v -count=1
```

Use `-count=3` instead of `-count=1` to reproduce the development-host
three-invocation shape above. The baseline and control arms produce new
outcomes; only the saved-schedule arm has a schedule ID and a replayable
command. The runner observations can be extracted from each smoke log with:

```bash
gh run view <run-id> --log \
  | grep -o 'MATCHING_BASELINE_COMPARE.*'
```

The workflow assertion step also prints its regular expression. Only the
marker emitted by `TestBaselineComparison` is a measured result.

## Evidence boundary

This experiment does not show that an ordinary integration test can never find
the defect. The hand-placed arms are intentionally offered as cross-environment
evidence only for the measured variation: `plain`, the 2 ms stagger, and the
20 ms stagger produced different counts between the development host and at
least one runner invocation. The measurements do not isolate host hardware
from runner load, image build, or any other environment difference, and they
do not establish a general detection rate or timing threshold.

The results that reproduced across both recorded environments are the
schedule-independent serial control at 0/100 in every invocation and the saved
schedule at 20/20 in every invocation. The hinted 2 ms arm happened to detect
100/100 and the 100 ms stagger happened to detect 0/100 in every tabled
invocation, but those hand-placed timing observations are not promoted to
cross-host guarantees. A saved schedule does not need to estimate transaction
duration because it controls declared ordering directly.

Within the measured fixture, `control_serial=0/100` is empirical evidence that
overlap is necessary for this violation. The development host's 300/300 across
the two concurrent arms and 2 ms stagger is bounded to that host. Across those
same arms, runner `32457121871` detected 100/100 for plain, 100/100 for hinted
delay, and 35/100 for the 2 ms stagger. Neither result proves that overlap under
`REPEATABLE READ` is sufficient in isolation. Such a claim requires an
isolation-level control such as READ COMMITTED versus REPEATABLE READ, which is
outside this experiment.

Smoke continues to accept `[0-9]+` for counts in the timing-sensitive arms.
An exact count or tolerance would turn environment-dependent observations into
a regression contract. The Go test fixes each denominator at 100 and fails on
worker errors or deadlocks; smoke checks the marker shape and fixed
denominators, while retaining hard assertions for `control_serial=0/100` and
`schedule_replay=20/20`.

The baseline arms do not leave a schedule ID, a replay command for the observed
ordering, or verdict artifacts. The weavegate distinction measured here is
that the committed schedule controlled and repeated the same execution in both
recorded environments and can be used by the CLI to retain evidence; it is not
a claim that only weavegate can detect the violation.
