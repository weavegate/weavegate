# External SUT v1 conformance plan (proposed)

These are constructed design examples for the
[v1 wire contract](external-sut-v1.md), not captured output or passing tests.
Neither a Go external adapter nor a Java SDK exists in this change. The shared
[JSON vectors](testdata/external-sut-v1.json) are the common input for both future
implementation issues; do not fork language-specific copies of the protocol.
[ADR 0010](../adr/0010-external-sut-protocol.md) lists the engine decisions that
must precede end-to-end enablement.

## Vector format and consumption

`vector_format: 1` describes this test-data format; `wire_version: 1` describes
the transport. Every case has a stable `id`, a named `prefix`, ordered `steps`,
and descriptive `covers` tags. Expand `prefixes` recursively before the steps.
A prefix element containing `prefix` expands another named history; all other
elements are steps. Prefixes are finite histories, not arbitrary internal-state
snapshots. Every case starts with fresh peers and fake controllable clocks.

A step's `peer` is the receiver/observer (`go` or `java`). `action: receive`
injects the complete `frame` object. Serialize it as compact UTF-8 JSON and add
the four-byte byte-length header. Preserve the same serialization for an exact
duplicate. `action: local` injects a named lifecycle event with `args` through a
harness seam (runtime release, JDBC barrier, proxy exit, lease return, clock
advance, stderr, or child exit). These events and `expect` labels are test
vocabulary, never wire message types. Receipt can deliberately inject a faulty
peer message, so harnesses must not generate additional implicit receive steps
from a `send_*` expectation.

`expect` lists required observable effects in order; the assertions named
`no_*`, `ignore_*`, and `drop_*` forbid the corresponding side effect. For
example, `client_arrive_once` increments the Go runtime-call count once;
`wake_exact_gate` resumes only the identified Java gate; `no_worker_result`
forbids emitting any WorkerResult for the affected incomplete invocation.
`abort_run` requires a run error, not an ordinary worker failure or a verdict.
`invalidate_evaluation` rejects even a previously obtained passing evaluation.
`quarantine_fixture` requires `reset_rejected` until teardown/reprovisioning.
A `completion` event supplies independently observed proxy-exit, transaction,
and lease state; the harness must not treat it as a request to publish terminal.

Go tests exercise Go steps against a scripted child and runtime double; Java
tests exercise Java steps against a scripted engine and controllable command/
DataSource. Each consumes the other peer's steps as the scripted conversation.
The combined integration runner exercises both roles. Preserve per-worker
causality; no total order between independent workers is inferred from pipe
traffic. Production IDs are random; the fixed hex IDs and credentials here are
synthetic test inputs only.

`framing` cases bypass object serialization: feed exactly `input_hex` bytes to
the decoder. `read_chunk_sizes` splits the leading reads; feed any remainder in
one final read. `eof: true` closes the stream afterward. Compare a valid decoded
object to `decoded` and require no dispatch before the entire payload arrives.
Also run valid frames coalesced and at every single byte boundary. Invalid cases
must fail before invoking application code.

A language implementation must report each case ID and its result. Unsupported
cases remain failing/incomplete acceptance work, not silently skipped coverage.
This PR validates the data's syntax and internal references only; executing
protocol semantics and publishing evidence markers belongs to the implementation
issues. When those tests add fixed-phrase markers, add their exact CI checks in
the same implementation PR.

## Sequence review

All sequences below are constructed. `E` is the Go adapter, `J` the JVM SDK,
`R` the Go runtime, and `DB` the fixture database. Successful startup is
`E start → J initialize/validate/probe/return probe lease → J ready → E Invoke`.

| Case | Causal sequence | Required observation |
| --- | --- | --- |
| Success | Invoke → accepted → transaction begin → arrive → R.Arrive blocks → orchestrator releases → R.Arrive returns nil → targeted release → commit → proxy exit + lease return → terminal → oracle → stop/stopped/EOF/exit 0 | Exactly one result after cleanup; oracle alone judges database invariants. |
| Rollback | Invoke → accepted → arrive/release → command exception → proxy rolls back → lease return → terminal with error | Rolled-back outcome, error preserved, no early completion from a callback. |
| Database blocking | w1 holds row lock at an arrival; w2's SQL blocks before its arrival → Go WaitArrive timeout → release w1 → w1 commits → w2 reaches its point | Go infers blocking; pipe reader stays available and does not release w2 until its own runtime call returns nil. No Java `db_blocked` message. |
| Cancellation | Go context cancels bridge + sends cancel → Java gate throws → proxy rolls back → lease returns → terminal(cancelled) | No release from cancellation; context failure remains a failed operation. Already-committed work reports committed truthfully. |
| Process death | JVM disappears with a transaction/arrival outstanding → EOF/exit observed → session fault → bounded cleanup/quarantine | No invented WorkerResult; no oracle success, reset, or retry on the uncertain database. Death after terminal but before normal Stop also fails the run. |
| Stale arrival | Session A or invocation i1 retires; session B or invocation i2 uses w1 → delayed A/i1 arrive/release | Old identity cannot call B's runtime or resume i2. Session mismatch does not advance B's sequence; retired invocation messages consume current sequence without side effects. |

`stop_before_ready` exercises the alternate startup path: E sends start (1),
requests Stop and sends stop (2), then receives J's first frame stopped (1)
after cleanup. EOF and exit 0 complete Stop without returning a Handle or running
a command. `unsolicited_startup_stopped` rejects the same first J frame when E
has not requested Stop.

`cancel_cleanup_deadline` explicitly cancels the Go invocation and unwinds its
bridge, delivers Java's cleanup fatal to Go, and invokes Go Stop with a 5000 ms
budget before advancing its halfway/deadline events. The stop frame is delivered
to Java explicitly as well. These are distinct fake-clock events: Java's cancel
grace expiring does not establish Go's stop deadline, and a fatal-send
expectation cannot stand in for Go receiving that frame. The case intentionally
leaves child exit/reaping unproven to require a Stop error and reject Reset.

## Implementation checklist

The consumers are [Go adapter #108](https://github.com/weavegate/weavegate/issues/108)
and [Java/Spring SDK #109](https://github.com/weavegate/weavegate/issues/109).
Both implementation PRs must link this checklist and the same vector file at the
reviewed commit, copy the relevant acceptance items into their validation plan,
and record results against that revision.
[CLI integration #110](https://github.com/weavegate/weavegate/issues/110) owns
configuration enablement; [Spring evidence #111](https://github.com/weavegate/weavegate/issues/111)
owns the combined MySQL reproduction. Do not mark this design checklist complete
on the strength of prose or a mock-only test.

- [ ] Resolve ADR gaps G1–G4 in separately reviewable decisions before enabling external CLI execution: fixture descriptor, asynchronous fault propagation, reset quarantine, and launch/config/budgets.
- [ ] Consume all shared vector IDs. Go owns runtime mapping, channel closure, process supervision, and error propagation; Java owns framing, dispatch, gates, proxy/lease tracking, and local cancellation. Both test malformed input and duplicate handling.
- [ ] Implement only child-JVM launch with framed stdin/stdout. Test fragmented/coalesced frames, invalid JSON/UTF-8/fields/version, unknown names/IDs, gaps, conflicting duplicates, sequence exhaustion, and capacity exhaustion without changing application state on rejection.
- [ ] Exercise concurrent arrivals and releases with barriers, including a blocked worker while another commits. The pipe reader/writer must remain live; no sleep-based coordination or polling for readiness.
- [ ] Prove fresh sessions for repeats/exploration, stale-session isolation, invocation identity on worker reuse, and no second command execution on duplicate input. Test immediate repeated point rejection remains consistent with the Go runtime.
- [ ] Against the pinned Spring proxy, transaction manager, Connector/J and pool, prove success, rollback, transaction-begin failure, rollback-only, after-commit exception, unknown commit/rollback, and suppressed connection-close failure. Record transaction and lease milestones separately; no terminal before proxy exit and successful lease return.
- [ ] Prove disabled Java instrumentation returns immediately from sync points without reading stdin, launching a protocol loop, or changing transaction behavior.
- [ ] Validate transaction registration and reject unsupported propagation, self-invocation bypass, extra DataSources, background DB writers, and async commands. Explicit test instrumentation must not alter the business transaction's intended invariant.
- [ ] Test cancel before accepted, at arrival, in blocked JDBC, during commit, and after terminal. Prove cancellation cannot be undone by release. Driver cancel requests alone cannot satisfy completion.
- [ ] Test startup failure/deadline, no ready, disconnect at each phase, silent child, blocked pipe writer, child death before and after terminal, nonzero exit, missing stopped, hung shutdown, idempotent Stop, and bounded forced termination/reaping. No elapsed phase can restart the deadline.
- [ ] Verify fatal faults interrupt execution and oracle evaluation, invalidate provisional success, and preserve nonzero run failure. Worker errors (including MySQL 1213/1205) remain terminal facts for existing oracles; no new verdict logic lives in the adapter.
- [ ] Prove Reset follows normal Stop and cannot run after uncertain cleanup until fixture teardown/reprovisioning. Verify no old connection, transaction, JVM thread, or queued bridge call survives into a later schedule.
- [ ] Verify readiness uses the fixture-provided endpoint and application account; probe lease returned, migration/reset owned only by Go, adequate worker/pool capacity, no credentials in argv/environment/artifacts/errors, bounded stderr, and stdout reserved before Boot startup.
- [ ] Add end-to-end vulnerable/fixed MySQL evidence with repeated runs (`-count=N` for Go) and record exact commands, counts, toolchain/dependency versions, and markers. Do not claim deterministic external replay from one pass or adjust waits to hide instability.
- [ ] Update public config/instrumentation docs only when implementation ships. If a new diagnostic is chosen, add its matching reference page and index entry in that implementation PR.
