package io.github.weavegate.sdk;

import static org.assertj.core.api.Assertions.assertThat;

import java.sql.SQLException;
import java.util.stream.Stream;

import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;

/** Independent regressions; these do not promote unimplemented shared acceptance rows. */
class PeerLifecycleTest {
    @TestFactory
    Stream<DynamicTest> startupAndStopRequireReturnedLeases() {
        return RequirementsTest.repeated(() -> {
            for (boolean stop : new boolean[] {false, true}) {
                VectorHarness h = new VectorHarness("independent").quiet();
                h.host.shutdown.signal();
                h.peer.receive(Scripted.start(1));
                h.activity.awaitIdle();
                h.peer.leaseAcquired(null); // Application startup retained an extra lease.
                if (stop) {
                    h.peer.receive(Scripted.stop(2, 2500));
                } else {
                    h.host.completeProbe();
                }
                h.activity.awaitIdle();
                assertThat(h.output.frames).extracting(f -> f.get("type").stringValue())
                        .doesNotContain("ready", "stopped");
                assertThat(h.peer.fatalKind).isNotNull();
                assertThat(h.exit.status()).isEqualTo(1);
            }
        });
    }

    @TestFactory
    Stream<DynamicTest> terminalEnqueueFailureRetiresOnlyOnce() {
        return RequirementsTest.repeated(() -> {
            for (boolean exhausted : new boolean[] {false, true}) {
                VectorHarness h = Scripted.ready(2);
                h.peer.receive(Scripted.invoke(2, Scripted.I1, "w1", "assign"));
                h.peer.receive(Scripted.invoke(3, Scripted.I2, "w2", "assign"));
                h.threads.run(Scripted.I2);
                h.activity.awaitIdle();
                if (exhausted) {
                    h.peer.sent = Wire.MAX_SEQ;
                } else {
                    h.output.closed = true;
                }
                Scripted.complete(h, Scripted.I1, Peer.Transaction.COMMITTED);
                assertThat(h.peer.live).isEqualTo(1);
                assertThat(h.peer.cleanupStarted).isFalse();
                assertThat(h.peer.invocations.get(Scripted.I2).retired).isFalse();
                h.host.shutdown.signal();
                Scripted.complete(h, Scripted.I2, Peer.Transaction.ROLLED_BACK);
                h.activity.awaitIdle();
                assertThat(h.peer.live).isZero();
                assertThat(h.exit.status()).isEqualTo(1);
            }
        });
    }

    @TestFactory
    Stream<DynamicTest> driverErrorsDoNotExposeSqlValues() {
        return RequirementsTest.repeated(() -> {
            SQLException original = new SQLException("Duplicate entry 'secret-value' for SQL INSERT INTO seat", "23000", 1062);
            var error = Peer.classify(TrackingDataSource.driverSummary(new SQLException("translated secret-value", original)));
            assertThat(error.get("kind").stringValue()).isEqualTo("mysql");
            assertThat(error.get("mysql_code").intValue()).isEqualTo(1062);
            assertThat(error.get("sql_state").stringValue()).isEqualTo("23000");
            assertThat(error.toString()).doesNotContain("secret-value", "INSERT INTO");
            assertThat(Peer.classify(TrackingDataSource.driverSummary(new SQLException("jdbc:mysql://secret-value"))).toString())
                    .doesNotContain("secret-value", "jdbc:");
            assertThat(original.getMessage()).contains("secret-value");
        });
    }
}
