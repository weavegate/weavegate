package io.github.weavegate.sdk;

import java.io.PrintWriter;
import java.lang.reflect.InvocationHandler;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.lang.reflect.Proxy;
import java.sql.CallableStatement;
import java.sql.Connection;
import java.sql.DatabaseMetaData;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.SQLFeatureNotSupportedException;
import java.sql.Statement;
import java.util.logging.Logger;

import javax.sql.DataSource;

/**
 * The fixture DataSource as seen by the application. It records each lease's
 * acquisition and successful delegate {@code close()}, including close failures
 * that Spring's cleanup logs and suppresses, and registers executing statements
 * for cancellation.
 */
final class TrackingDataSource implements DataSource {
    private final DataSource delegate;
    private final Peer peer;

    TrackingDataSource(DataSource delegate, Peer peer) {
        this.delegate = delegate;
        this.peer = peer;
    }

    @Override
    public Connection getConnection() throws SQLException {
        try {
            return track(delegate.getConnection());
        } catch (SQLException e) {
            recordDriverFailure(e);
            throw e;
        }
    }

    @Override
    public Connection getConnection(String username, String password) throws SQLException {
        throw new SQLFeatureNotSupportedException("fixture credentials are fixed");
    }

    private Connection track(Connection connection) {
        Peer.Invocation invocation = Peer.current();
        peer.leaseAcquired(invocation);
        return (Connection) Proxy.newProxyInstance(Connection.class.getClassLoader(), new Class<?>[] {Connection.class},
                new Lease(connection, invocation));
    }

    private final class Lease implements InvocationHandler {
        private final Connection connection;
        private final Peer.Invocation invocation;
        private boolean closed;

        Lease(Connection connection, Peer.Invocation invocation) {
            this.connection = connection;
            this.invocation = invocation;
        }

        @Override
        public Object invoke(Object proxy, Method method, Object[] args) throws Throwable {
            switch (method.getName()) {
                case "unwrap" -> {
                    return unwrapTracked(proxy, (Class<?>) args[0]);
                }
                case "isWrapperFor" -> {
                    return ((Class<?>) args[0]).isInstance(proxy);
                }
                case "close" -> {
                    synchronized (this) {
                        if (closed) {
                            return null;
                        }
                        closed = true;
                    }
                    try {
                        connection.close();
                    } catch (Throwable t) {
                        peer.leaseCloseFailed(invocation);
                        throw t;
                    }
                    peer.leaseReturned(invocation);
                    return null;
                }
                case "isClosed" -> {
                    synchronized (this) {
                        if (closed) {
                            return true;
                        }
                    }
                }
                case "equals" -> {
                    return proxy == args[0];
                }
                case "hashCode" -> {
                    return System.identityHashCode(proxy);
                }
                case "toString" -> {
                    return "TrackedConnection";
                }
                default -> {
                }
            }
            Object result = call(connection, method, args);
            if (result instanceof Statement statement) {
                return trackStatement(statement, invocation, (Connection) proxy);
            }
            if (result instanceof DatabaseMetaData metadata) {
                return Proxy.newProxyInstance(DatabaseMetaData.class.getClassLoader(),
                        new Class<?>[] {DatabaseMetaData.class}, (p, m, a) -> {
                            return switch (m.getName()) {
                                case "getConnection" -> proxy;
                                case "unwrap" -> unwrapTracked(p, (Class<?>) a[0]);
                                case "isWrapperFor" -> ((Class<?>) a[0]).isInstance(p);
                                default -> {
                                    Object value = call(metadata, m, a);
                                    if (value instanceof ResultSet rows) {
                                        Statement owner = rows.getStatement();
                                        yield trackRows(rows, owner == null ? null
                                                : trackStatement(owner, invocation, (Connection) proxy));
                                    }
                                    yield value;
                                }
                            };
                        });
            }
            return result;
        }
    }

    private Statement trackStatement(Statement statement, Peer.Invocation invocation, Connection connection) {
        Class<?> type = statement instanceof CallableStatement ? CallableStatement.class
                : statement instanceof PreparedStatement ? PreparedStatement.class : Statement.class;
        return (Statement) Proxy.newProxyInstance(Statement.class.getClassLoader(), new Class<?>[] {type},
                new Executing(statement, invocation, connection));
    }

    private static ResultSet trackRows(ResultSet rows, Statement statement) {
        return (ResultSet) Proxy.newProxyInstance(ResultSet.class.getClassLoader(), new Class<?>[] {ResultSet.class},
                (proxy, method, args) -> switch (method.getName()) {
                    case "getStatement" -> statement;
                    case "unwrap" -> unwrapTracked(proxy, (Class<?>) args[0]);
                    case "isWrapperFor" -> ((Class<?>) args[0]).isInstance(proxy);
                    default -> call(rows, method, args);
                });
    }

    private static Object unwrapTracked(Object proxy, Class<?> type) throws SQLException {
        if (type.isInstance(proxy)) {
            return proxy;
        }
        throw new SQLFeatureNotSupportedException("vendor JDBC unwrapping bypasses weavegate tracking");
    }

    private final class Executing implements InvocationHandler {
        private final Statement statement;
        private final Peer.Invocation invocation;
        private final Connection connection;

        Executing(Statement statement, Peer.Invocation invocation, Connection connection) {
            this.statement = statement;
            this.invocation = invocation;
            this.connection = connection;
        }

        @Override
        public Object invoke(Object proxy, Method method, Object[] args) throws Throwable {
            switch (method.getName()) {
                case "getConnection" -> { return connection; }
                case "unwrap" -> { return unwrapTracked(proxy, (Class<?>) args[0]); }
                case "isWrapperFor" -> { return ((Class<?>) args[0]).isInstance(proxy); }
                default -> { }
            }
            if (invocation == null || !method.getName().startsWith("execute")) {
                Object value = call(statement, method, args);
                return value instanceof ResultSet rows ? trackRows(rows, (Statement) proxy) : value;
            }
            Seams.Cancellable cancel = statement::cancel;
            try {
                if (!peer.statementStarted(invocation, cancel)) {
                    throw new WeavegateCancelledException("cancelled before statement");
                }
                Object value = call(statement, method, args);
                return value instanceof ResultSet rows ? trackRows(rows, (Statement) proxy) : value;
            } finally {
                peer.statementFinished(invocation, cancel);
            }
        }
    }

    private static Object call(Object target, Method method, Object[] args) throws Throwable {
        try {
            return method.invoke(target, args);
        } catch (InvocationTargetException e) {
            if (e.getCause() instanceof SQLException sql) {
                recordDriverFailure(sql);
            }
            throw e.getCause();
        }
    }

    private static void recordDriverFailure(SQLException failure) {
        Peer.Invocation invocation = Peer.current();
        if (invocation != null) {
            invocation.peer().recordSource(invocation, driverSummary(failure));
        }
    }

    /** Keep driver detail in the original application exception, never in wire evidence. */
    static SQLException driverSummary(SQLException failure) {
        var classified = Peer.classify(failure);
        boolean mysql = classified.get("kind").stringValue().equals("mysql");
        return new SQLException(mysql ? "MySQL operation failed" : "database operation failed",
                classified.get("sql_state").stringValue(), classified.get("mysql_code").intValue());
    }

    @Override
    public PrintWriter getLogWriter() throws SQLException {
        return delegate.getLogWriter();
    }

    @Override
    public void setLogWriter(PrintWriter out) throws SQLException {
        delegate.setLogWriter(out);
    }

    @Override
    public void setLoginTimeout(int seconds) throws SQLException {
        delegate.setLoginTimeout(seconds);
    }

    @Override
    public int getLoginTimeout() throws SQLException {
        return delegate.getLoginTimeout();
    }

    @Override
    public Logger getParentLogger() throws SQLFeatureNotSupportedException {
        return delegate.getParentLogger();
    }

    @Override
    public <T> T unwrap(Class<T> type) throws SQLException {
        if (type.isInstance(this)) {
            return type.cast(this);
        }
        throw new SQLException("tracked DataSource cannot be unwrapped");
    }

    @Override
    public boolean isWrapperFor(Class<?> type) {
        return type.isInstance(this);
    }
}
