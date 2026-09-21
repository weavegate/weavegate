# Java Spring external SUT peer (in development)

[`sdk/java`](../../sdk/java/) implements the Java peer of
[wire v1](external-sut-v1.md) for an explicitly instrumented Spring Boot test
application. It is not published as a package, the `weavegate` CLI does not
select it, and its complete Java acceptance gate has not passed.

## Supported baseline

The Maven build enforces one tested baseline. Versions come from the Spring Boot
parent's dependency management; the build fails on other Java or Maven versions.

| Component | Version | Purpose |
| --- | --- | --- |
| Java | 21 | Project baseline selected by [ADR 0010](../adr/0010-external-sut-protocol.md) |
| Spring Boot / Framework | 4.0.8 / 7.0.9 | Non-web application context, `@Transactional` proxies |
| Transaction manager | `DataSourceTransactionManager` (spring-jdbc 7.0.9) | Begin, commit, rollback and connection cleanup |
| Pool | HikariCP 7.0.2 | The single fixture DataSource |
| JDBC driver | MySQL Connector/J 9.7.0 | MySQL 8.4 fixture access and statement cancellation |
| Codec | Jackson 3.1.5 | Strict JSON with duplicate-key and trailing-value rejection |
| Build | Apache Maven 3.9.16 through `./mvnw`, checksum-pinned ZIP (`unzip` required for bootstrap) | Reproducible build and test runner |

Test-only dependencies are Spring Boot's test starter, the JUnit launcher API
for execution evidence and Testcontainers for a real MySQL 8.4 server.

## Opting in

Instrumentation is inactive unless the child JAR's `main` method calls the
bootstrap. An ordinary `SpringApplication.run` never reads stdin, starts a
protocol thread or changes transactions, and inactive sync points return
immediately.

```java
public final class InstrumentedMain {
    public static void main(String[] args) {
        WeavegateChild.run(MyApplication.class, args);
    }
}
```

The bootstrap reserves stdout for frames before Spring Boot starts, redirects
`System.out` to stderr and disables the banner. It reads `start`, then builds a
non-web context with the fixture DataSource and a transaction manager supplied
by the SDK. Credentials arrive only in the start frame. SQL initialization,
Flyway and Liquibase are disabled. Startup fails if the context contains another
DataSource or transaction manager, or enables `@Scheduled` or `@Async`
processing; processor beans are detected by type, regardless of bean name.

Commands are public, non-final instance methods on proxied Spring beans. The bean
class itself may be package-private; the selected proxy method is made reflectively
accessible before dispatch. Static methods and proxies without accessible,
matching transaction advice are rejected.
Each needs one `@Transactional` boundary with `REQUIRED` propagation, no
wall-clock timeout, and rollback behavior for `WeavegateCancelledException`
(the default rule for runtime exceptions does).
The dispatcher calls the bean proxy, so self-invocation cannot bypass it.

```java
@Service
public class SeatCommands {
    private final JdbcTemplate jdbc;

    public SeatCommands(JdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    @Transactional
    @WeavegateCommand(value = "assign", points = {"after_read"})
    public void assign(CommandContext context) {
        jdbc.queryForObject("SELECT taken_by FROM seat WHERE id = 1 FOR UPDATE", String.class);
        Weavegate.syncPoint("after_read");
        jdbc.update("UPDATE seat SET taken_by = ? WHERE id = 1", context.worker());
    }
}
```

Readiness validates every requested command and point against the selected
command registrations, retains each command's declared point set, then completes
a database probe that returns its lease. Every runtime arrival must be both
requested for the session and declared by the invoked command.
All application-startup leases must also be returned before ready; a lease left
after application closure prevents normal stopped. One
invocation runs on one worker thread with one transaction and one connection
lease. Nested transactions, `REQUIRES_NEW` suspension, a second lease, database
use outside an invocation after readiness, retained JDBC handle use from another
thread and sync points outside the worker's proxy call are session failures.
Application calls to JDBC auto-commit, commit, rollback or savepoint controls
are rejected through both connection methods and direct, prepared or batched SQL;
only the SDK-owned transaction manager may use them. SQL that can commit
implicitly or act outside the transaction, including DDL, table locks, account
management and administrative statements, is rejected before JDBC delegation
as a fatal unsupported adapter operation. Nonzero JDBC statement query timeouts
are unsupported because they would make a schedule depend on wall-clock time;
zero continues to mean no timeout.

Standard JDBC `unwrap` returns the tracking proxy when that interface is
supported; vendor-specific unwrapping is rejected. Statement, result-set and
metadata navigation retain tracked handles, including `getConnection()` and
`getStatement()`. Blocking result-set navigation and close operations register
their owning statement for cancellation, so streaming row drains cannot bypass
statement cancellation.
After the first start binds the session identity, every schema-valid foreign
frame is stale-dropped before direction and sequence checks.

## Completion and cancellation

`Weavegate.syncPoint` installs the gate and sends `arrive` under the peer's
serialization point, then blocks without a timeout. Only a release for the exact
invocation, worker, arrival and point wakes it. Cancellation latches its first
reason irreversibly, wakes the gate with `WeavegateCancelledException`, arms the
`cancel_ms` watchdog and requests cancellation of executing JDBC statements. A
release racing cancellation is consumed without resuming the worker.

A terminal is sent only after three independent milestones: the command proxy
returned or threw, the transaction manager recorded commit or rollback, and the
tracked connection's `close()` returned. Spring runs completion callbacks before
it returns the connection, so callbacks are never terminal evidence. A failed
commit or rollback is an unknown outcome and sends a transaction fatal. A
connection close failure that Spring logs and suppresses sends a cleanup fatal.
Neither case produces a terminal. An exception after commit reports `committed`
with an application error. MySQL vendor code and SQLSTATE come from the
underlying `SQLException`, not Spring's translated message.
If transaction begin fails, its driver evidence is recorded before Spring
returns the lease, so a concurrent cancellation cannot replace the earlier
application or MySQL error.

The existing transaction interceptor remains responsible for transaction rules.
An observer immediately inside it records a command-body failure before Spring
starts cleanup. Synchronizations are wrapped before commit (and refreshed before
the driver commit for callbacks added during preparation), preserving callback
order and propagating their original exceptions. Only propagating before-commit
and after-commit failures become source evidence; Spring's suppressed completion
errors remain suppressed. This records failure versus cancellation order without
using callback completion as terminal evidence.

When a driver exception escapes the command boundary, it contributes a fixed
`MySQL operation failed` or `database operation failed` summary to wire evidence;
vendor code and SQLSTATE remain available. Application code still receives the
original driver exception, and an exception it catches does not become worker
failure evidence. Locally authored application exception messages must already
respect the wire contract's no-secrets/no-SQL-literals requirement.

Startup, cancellation, stop and post-fatal watchdogs force a nonzero exit at
their deadline without waiting for rollback, pool closure or shutdown hooks. A
later deadline never extends an earlier one. A watchdog fatal write is best
effort and bounded by 100 ms, after which the process halts even if stdout is
blocked. After fatal, no terminal or `stopped` is emitted; known cleanup
completes locally and the process exits 1. Exit 0 happens only after `stopped`
is flushed and stdout is closed. The peer remains STOPPING during this drain;
queue overflow, failed writes/flush/close, or an expired Stop deadline retain a
failed session and cannot produce exit 0.
The forced-halt path performs no stderr flush, because stderr backpressure
cannot be allowed to extend an absolute watchdog deadline.

## Validation and remaining acceptance

Run from `sdk/java` with Docker available for the MySQL and child-process tests:

```bash
./mvnw -B verify -Dweavegate.repetitions=20
```

Each repetition is a separate JUnit execution. A launcher listener writes
execution boundaries, versions and observer records to
`target/weavegate-evidence/java.log`. Record a manifest beside that log and the
captured Maven output:

```bash
mkdir -p /tmp/weavegate-java-evidence
(cd sdk/java && ./mvnw -B verify -Dweavegate.repetitions=20 \
  -Dweavegate.evidence=/tmp/weavegate-java-evidence/java.log) > /tmp/weavegate-java-evidence/build.log
python3 scripts/record-external-sut-java-results.py \
  --log /tmp/weavegate-java-evidence/java.log \
  --build-log /tmp/weavegate-java-evidence/build.log \
  --output /tmp/weavegate-java-evidence/java.json \
  --revision "$(git rev-parse HEAD)" \
  --command './mvnw -B verify -Dweavegate.repetitions=20 -Dweavegate.evidence=/tmp/weavegate-java-evidence/java.log'
python3 scripts/check-external-sut-acceptance.py --results /tmp/weavegate-java-evidence/java.json
python3 scripts/test-external-sut-java-results.py
```

The tests verify the pinned vector SHA-256 before execution. All 28 lifecycle
cases and 11 framing cases that target Java run against a scripted engine with
controllable host, clock, exit and threads. Unknown events, arguments,
assertions, exception classes and phases fail before injection. The recorder
accepts a check only if it occurs exactly once in every passing repetition of
the same test and names an existing Java method on its actual declaring type.
Nested types use their enclosing names (for example, `Outer.Inner.observe`);
methods of sibling or anonymous types cannot validate that reference. CI records
the exact Maven argument array, including the evidence path, beside its logs.

Independent tests run the production bootstrap in child JVMs over real pipes.
They cover the success lifecycle through `stopped`, stdout EOF and exit 0;
EOF during startup, active, post-terminal and Stop phases; and broken and
blocked writers. Spring tests use the pinned stack against MySQL 8.4. They
observe proxy exit, driver commit or rollback and physical close outside the
SDK, and inject begin, commit, rollback and close failures beneath lease
tracking.

The recorded manifest reports 46 passing rows and one incomplete row.
`requirement/java-wire-matrix` stays incomplete: its tests exist, but the
shared vectors do not yet contain the matrix cases that the row requires.
Adding them changes the pinned input. The strict `--require-complete` gate
therefore still fails, and
[#109](https://github.com/weavegate/weavegate/issues/109) stays open. CLI
launch and budget composition remain
[#110](https://github.com/weavegate/weavegate/issues/110); live paired MySQL
evidence remains [#111](https://github.com/weavegate/weavegate/issues/111).
