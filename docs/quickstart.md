# Quickstart

You need a running Docker daemon. The first run starts a real MySQL 8.4
container, so allow extra time if Docker must pull the image.

This tutorial uses the repository's reference fixture to reproduce a database
race, then replays the same schedule against its fixed implementation. It does
not modify the fixture or your database.

## 1. Clone the repository

Time: about one minute, depending on the network.

```bash
git clone https://github.com/weavegate/weavegate.git
cd weavegate
```

Expected result: the checkout contains
`fixtures/matching-slice/.weavegate/config.yaml`.

## 2. Build from source

Time: about one minute after Go has downloaded the module dependencies.

Use the same source installation path documented in the
[README](../README.md#from-source):

```bash
go build -o weavegate ./cmd/weavegate
export PATH="$PWD:$PATH"
```

Confirm that this checkout's binary is available:

```console
$ weavegate --help
Reach a verdict on a concurrent workflow and save the evidence.
```

Expected exit code: `0`.

Release archives provide another installation path, but require a published
release. See [From a release archive](../README.md#from-a-release-archive).

## 3. Reproduce the violation

Time: about 20 seconds after the MySQL image is available.

Run the intentionally vulnerable variant:

```bash
weavegate run --config fixtures/matching-slice/.weavegate/config.yaml \
  --scenario concurrent-assign --variant vulnerable
```

The run fails because its SQL oracle observes two active assignments for the
same request. The following excerpt is captured output; the run directory is a
volatile identifier and will differ:

```text
## weavegate: FAIL (RG001)
scenario: concurrent-assign | schedules explored: 1 | violating: sch_7dcb74b1e506
assertion: active-assignment-is-unique
flaky: false (repeat=20)
error[RG001]: invariant violated under a controlled schedule
  observed:  active-assignment-is-unique returned 1 row: active_assignment_count=2 project_request_id=42
```

Check the process status immediately after the run:

```console
$ echo $?
2
```

Expected exit code: `2`. Keep the `sch_7dcb74b1e506` schedule ID printed after
`violating:`; the next step reuses it.

## 4. Replay the fixed variant

Time: about 15 seconds after the MySQL image is available.

Replay that exact schedule 20 times against the implementation that locks the
request row before deciding whether to insert:

```bash
weavegate run --config fixtures/matching-slice/.weavegate/config.yaml \
  --scenario concurrent-assign --variant fixed \
  --replay sch_7dcb74b1e506 --repeat 20
```

Captured output:

```text
## weavegate: PASS
scenario: concurrent-assign | schedules explored: 0 | replayed: sch_7dcb74b1e506
flaky: false (repeat=20)
```

Check the status:

```console
$ echo $?
0
```

Expected exit code: `0`. The schedule that deterministically violated the
invariant now passes on every replay. See the
[determinism experiment](experiments/determinism.md#repeated-result) for the
recorded evidence and [RG001](reference/diagnostics/RG001.md) for the diagnostic
contract.
