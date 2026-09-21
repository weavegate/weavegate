package io.github.weavegate.sdk;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Savepoint;
import java.sql.Statement;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;
import java.util.stream.Stream;
import javax.sql.DataSource;

import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;
import org.springframework.test.util.ReflectionTestUtils;

class TrackingHandlesTest {
    interface VendorConnection extends Connection { }
    interface VendorStatement extends Statement { }

    @TestFactory
    Stream<DynamicTest> jdbcNavigationCannotEscapeTracking() {
        return RequirementsTest.repeated(() -> {
            VectorHarness h = new VectorHarness("independent").quiet();
            DataSource source = mock(DataSource.class);
            Connection raw = mock(VendorConnection.class);
            Statement statement = mock(VendorStatement.class);
            ResultSet rows = mock(ResultSet.class);
            when(source.getConnection()).thenReturn(raw);
            when(raw.createStatement()).thenReturn(statement);
            when(raw.unwrap(Connection.class)).thenReturn(raw);
            when(raw.unwrap(VendorConnection.class)).thenReturn((VendorConnection) raw);
            when(statement.getConnection()).thenReturn(raw);
            when(statement.unwrap(Statement.class)).thenReturn(statement);
            when(statement.unwrap(VendorStatement.class)).thenReturn((VendorStatement) statement);
            when(statement.executeQuery("synthetic")).thenReturn(rows);
            when(rows.getStatement()).thenReturn(statement);
            Connection tracked = new TrackingDataSource(source, h.peer).getConnection();
            assertThat(tracked.unwrap(Connection.class)).isSameAs(tracked);
            assertThat(tracked.isWrapperFor(VendorConnection.class)).isFalse();
            assertThatThrownBy(() -> tracked.unwrap(VendorConnection.class)).isInstanceOf(SQLException.class);
            Statement wrapped = tracked.createStatement();
            assertThat(wrapped.getConnection()).isSameAs(tracked);
            assertThat(wrapped.unwrap(Statement.class)).isSameAs(wrapped);
            assertThatThrownBy(() -> wrapped.unwrap(VendorStatement.class)).isInstanceOf(SQLException.class);
            assertThat(wrapped.executeQuery("synthetic").getStatement()).isSameAs(wrapped);
            tracked.close();
            assertThat(h.peer.openLeases).isZero();
        });
    }

    @TestFactory
    Stream<DynamicTest> caughtSqlExceptionsDoNotBecomeInvocationFailures() {
        return RequirementsTest.repeated(() -> {
            VectorHarness h = new VectorHarness("independent").quiet();
            DataSource source = mock(DataSource.class);
            Connection raw = mock(Connection.class);
            Statement statement = mock(Statement.class);
            SQLException duplicate = new SQLException("Duplicate entry secret", "23000", 1062);
            when(source.getConnection()).thenReturn(raw);
            when(raw.createStatement()).thenReturn(statement);
            when(statement.execute("synthetic")).thenThrow(duplicate);
            Peer.Invocation invocation = h.peer.new Invocation(Scripted.I1, "w1", "assign");
            ThreadLocal<Peer.Invocation> current = current();
            invocation.thread = Thread.currentThread();
            invocation.proxy = Peer.Proxy.INSIDE;
            current.set(invocation);
            try {
                Connection tracked = new TrackingDataSource(source, h.peer).getConnection();
                try {
                    assertThatThrownBy(() -> tracked.createStatement().execute("synthetic")).isSameAs(duplicate);
                    assertThat(invocation.source).isNull();
                    h.peer.recordSource(invocation, duplicate);
                    assertThat(invocation.source).isNotSameAs(duplicate);
                    assertThat(invocation.source.getMessage()).isEqualTo("MySQL operation failed");
                } finally {
                    tracked.close();
                }
            } finally {
                current.remove();
            }
        });
    }

    @TestFactory
    Stream<DynamicTest> resultSetNavigationRemainsCancellable() {
        return RequirementsTest.repeated(() -> {
            VectorHarness h = new VectorHarness("independent").quiet();
            DataSource source = mock(DataSource.class);
            Connection raw = mock(Connection.class);
            Statement statement = mock(Statement.class);
            ResultSet rows = mock(ResultSet.class);
            AtomicReference<Boolean> registeredDuringNext = new AtomicReference<>();
            Peer.Invocation invocation = h.peer.new Invocation(Scripted.I1, "w1", "assign");
            when(source.getConnection()).thenReturn(raw);
            when(raw.createStatement()).thenReturn(statement);
            when(statement.executeQuery("synthetic")).thenReturn(rows);
            when(rows.next()).thenAnswer(ignored -> {
                synchronized (h.peer) {
                    registeredDuringNext.set(!invocation.statements.isEmpty());
                }
                return false;
            });
            ThreadLocal<Peer.Invocation> current = current();
            invocation.thread = Thread.currentThread();
            invocation.proxy = Peer.Proxy.INSIDE;
            current.set(invocation);
            try {
                Connection tracked = new TrackingDataSource(source, h.peer).getConnection();
                Statement trackedStatement = tracked.createStatement();
                ResultSet trackedRows = trackedStatement.executeQuery("synthetic");
                assertThat(trackedRows.next()).isFalse();
                assertThat(registeredDuringNext.get()).isTrue();
                trackedRows.close();
                trackedStatement.close();
                tracked.close();
            } finally {
                current.remove();
            }
        });
    }

    @TestFactory
    Stream<DynamicTest> applicationTransactionControlsAreRejected() {
        return RequirementsTest.repeated(() -> {
            VectorHarness h = new VectorHarness("independent").quiet();
            DataSource source = mock(DataSource.class);
            Connection raw = mock(Connection.class);
            Savepoint savepoint = mock(Savepoint.class);
            when(source.getConnection()).thenReturn(raw);
            Connection tracked = new TrackingDataSource(source, h.peer).getConnection();
            assertThatThrownBy(() -> tracked.setAutoCommit(false)).isInstanceOf(SQLException.class);
            assertThatThrownBy(tracked::commit).isInstanceOf(SQLException.class);
            assertThatThrownBy(tracked::rollback).isInstanceOf(SQLException.class);
            assertThatThrownBy(tracked::setSavepoint).isInstanceOf(SQLException.class);
            assertThatThrownBy(() -> tracked.rollback(savepoint)).isInstanceOf(SQLException.class);
            assertThatThrownBy(() -> tracked.releaseSavepoint(savepoint)).isInstanceOf(SQLException.class);
            verify(raw, org.mockito.Mockito.never()).setAutoCommit(false);
            verify(raw, org.mockito.Mockito.never()).commit();
            verify(raw, org.mockito.Mockito.never()).rollback();
            verify(raw, org.mockito.Mockito.never()).setSavepoint();
            verify(raw, org.mockito.Mockito.never()).rollback(savepoint);
            verify(raw, org.mockito.Mockito.never()).releaseSavepoint(savepoint);
            tracked.close();
        });
    }

    @TestFactory
    Stream<DynamicTest> sqlTransactionControlsAreRejectedBeforeDelegation() {
        return RequirementsTest.repeated(() -> {
            VectorHarness h = new VectorHarness("independent").quiet();
            DataSource source = mock(DataSource.class);
            Connection raw = mock(Connection.class);
            Statement statement = mock(Statement.class);
            PreparedStatement prepared = mock(PreparedStatement.class);
            when(source.getConnection()).thenReturn(raw);
            when(raw.createStatement()).thenReturn(statement);
            when(raw.prepareStatement("COMMIT")).thenReturn(prepared);
            Peer.Invocation invocation = h.peer.new Invocation(Scripted.I1, "w1", "assign");
            ThreadLocal<Peer.Invocation> current = current();
            invocation.thread = Thread.currentThread();
            invocation.proxy = Peer.Proxy.INSIDE;
            current.set(invocation);
            try {
                Connection tracked = new TrackingDataSource(source, h.peer).getConnection();
                Statement trackedStatement = tracked.createStatement();
                assertThatThrownBy(() -> trackedStatement.execute("COMMIT")).isInstanceOf(SQLException.class);
                assertThatThrownBy(() -> trackedStatement.addBatch("/* fixture */ ROLLBACK"))
                        .isInstanceOf(SQLException.class);
                assertThatThrownBy(() -> tracked.prepareStatement("COMMIT")).isInstanceOf(SQLException.class);
                assertThatThrownBy(() -> trackedStatement.execute("SET SESSION autocommit = 1"))
                        .isInstanceOf(SQLException.class);
                assertThatThrownBy(() -> trackedStatement.execute("SAVEPOINT fixture"))
                        .isInstanceOf(SQLException.class);
                verify(statement, org.mockito.Mockito.never()).execute("COMMIT");
                verify(statement, org.mockito.Mockito.never()).addBatch("/* fixture */ ROLLBACK");
                verify(raw, org.mockito.Mockito.never()).prepareStatement("COMMIT");
                verify(statement, org.mockito.Mockito.never()).execute("SET SESSION autocommit = 1");
                verify(statement, org.mockito.Mockito.never()).execute("SAVEPOINT fixture");
                tracked.close();
            } finally {
                current.remove();
            }
        });
    }

    @TestFactory
    Stream<DynamicTest> retainedJdbcHandlesRejectForeignThreads() {
        return RequirementsTest.repeated(() -> {
            VectorHarness h = new VectorHarness("independent").quiet();
            DataSource source = mock(DataSource.class);
            Connection raw = mock(Connection.class);
            Statement statement = mock(Statement.class);
            ResultSet rows = mock(ResultSet.class);
            when(source.getConnection()).thenReturn(raw);
            when(raw.createStatement()).thenReturn(statement);
            when(statement.executeQuery("query")).thenReturn(rows);
            Peer.Invocation invocation = h.peer.new Invocation(Scripted.I1, "w1", "assign");
            ThreadLocal<Peer.Invocation> current = current();
            invocation.thread = Thread.currentThread();
            invocation.proxy = Peer.Proxy.INSIDE;
            current.set(invocation);
            try {
                Connection tracked = new TrackingDataSource(source, h.peer).getConnection();
                Statement trackedStatement = tracked.createStatement();
                ResultSet trackedRows = trackedStatement.executeQuery("query");
                AtomicReference<Throwable> connectionFailure = invokeOffThread(tracked::getAutoCommit);
                AtomicReference<Throwable> statementFailure = invokeOffThread(() -> trackedStatement.execute("work"));
                AtomicReference<Throwable> rowsFailure = invokeOffThread(trackedRows::next);
                assertThat(connectionFailure.get()).isInstanceOf(WeavegateCancelledException.class);
                assertThat(statementFailure.get()).isInstanceOf(WeavegateCancelledException.class);
                assertThat(rowsFailure.get()).isInstanceOf(WeavegateCancelledException.class);
                assertThat(h.peer.fatalKind).isEqualTo("protocol");
                verify(raw, org.mockito.Mockito.never()).getAutoCommit();
                verify(statement, org.mockito.Mockito.never()).execute("work");
                verify(rows, org.mockito.Mockito.never()).next();
                trackedRows.close();
                trackedStatement.close();
                tracked.close();
            } finally {
                current.remove();
            }
        });
    }

    private interface SqlCall {
        Object call() throws Exception;
    }

    private static AtomicReference<Throwable> invokeOffThread(SqlCall call) throws InterruptedException {
        AtomicReference<Throwable> failure = new AtomicReference<>();
        Thread thread = new Thread(() -> {
            try {
                call.call();
            } catch (Throwable t) {
                failure.set(t);
            }
        });
        thread.start();
        thread.join(TimeUnit.SECONDS.toMillis(20));
        assertThat(thread.isAlive()).isFalse();
        return failure;
    }

    @SuppressWarnings("unchecked")
    private static ThreadLocal<Peer.Invocation> current() {
        return (ThreadLocal<Peer.Invocation>) ReflectionTestUtils.getField(Peer.class, "CURRENT");
    }
}
