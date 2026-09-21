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
import java.util.regex.Pattern;

import javax.sql.DataSource;

/**
 * The fixture DataSource as seen by the application. It records each lease's
 * acquisition and successful delegate {@code close()}, including close failures
 * that Spring's cleanup logs and suppresses, and registers blocking JDBC calls
 * for cancellation.
 */
final class TrackingDataSource implements DataSource {
    private static final Pattern TRANSACTION_SQL = Pattern.compile("(?is)^(?:BEGIN\\b|START\\s+TRANSACTION\\b|"
            + "COMMIT\\b|ROLLBACK\\b|SAVEPOINT\\b|RELEASE\\s+SAVEPOINT\\b|"
            + "SET\\s+(?:(?:SESSION|LOCAL|GLOBAL)\\s+)?TRANSACTION\\b|"
            + "SET\\s+(?:(?:SESSION|LOCAL|GLOBAL)\\s+)?"
            + "(?:@@\\s*(?:(?:SESSION|LOCAL|GLOBAL)\\s*\\.\\s*)?)?AUTOCOMMIT\\b|"
            + "XA\\s+(?:START|BEGIN|END|PREPARE|COMMIT|ROLLBACK)\\b)");
    private static final ThreadLocal<Integer> TRANSACTION_CONTROL = new ThreadLocal<>();
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
            observeDriverFailure(e);
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
            requireInvocation(invocation);
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
            if (transactionControl(method.getName()) && !transactionControlAllowed()) {
                throw new SQLFeatureNotSupportedException("application-managed transaction control is unsupported");
            }
            rejectTransactionSql(method, args);
            Object result = call(connection, method, args);
            if (result instanceof Statement statement) {
                return trackStatement(statement, invocation, (Connection) proxy);
            }
            if (result instanceof DatabaseMetaData metadata) {
                return Proxy.newProxyInstance(DatabaseMetaData.class.getClassLoader(),
                        new Class<?>[] {DatabaseMetaData.class}, (p, m, a) -> {
                            requireInvocation(invocation);
                            return switch (m.getName()) {
                                case "getConnection" -> proxy;
                                case "unwrap" -> unwrapTracked(p, (Class<?>) a[0]);
                                case "isWrapperFor" -> ((Class<?>) a[0]).isInstance(p);
                                default -> {
                                    Object value = call(metadata, m, a);
                                    if (value instanceof ResultSet rows) {
                                        Statement owner = rows.getStatement();
                                        Statement tracked = owner == null ? null
                                                : trackStatement(owner, invocation, (Connection) proxy);
                                        yield trackRows(rows, tracked, invocation,
                                                owner == null ? null : owner::cancel);
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

    private ResultSet trackRows(ResultSet rows, Statement statement, Peer.Invocation invocation,
                                Seams.Cancellable cancel) {
        return (ResultSet) Proxy.newProxyInstance(ResultSet.class.getClassLoader(), new Class<?>[] {ResultSet.class},
                (proxy, method, args) -> {
                    requireInvocation(invocation);
                    switch (method.getName()) {
                        case "getStatement" -> { return statement; }
                        case "unwrap" -> { return unwrapTracked(proxy, (Class<?>) args[0]); }
                        case "isWrapperFor" -> { return ((Class<?>) args[0]).isInstance(proxy); }
                        case "close" -> { return call(rows, method, args); }
                        default -> { }
                    }
                    if (invocation == null || cancel == null) {
                        return call(rows, method, args);
                    }
                    try {
                        if (!peer.statementStarted(invocation, cancel)) {
                            throw new WeavegateCancelledException("cancelled before result-set navigation");
                        }
                        return call(rows, method, args);
                    } finally {
                        peer.statementFinished(invocation, cancel);
                    }
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
        private final Seams.Cancellable cancel;

        Executing(Statement statement, Peer.Invocation invocation, Connection connection) {
            this.statement = statement;
            this.invocation = invocation;
            this.connection = connection;
            this.cancel = statement::cancel;
        }

        @Override
        public Object invoke(Object proxy, Method method, Object[] args) throws Throwable {
            requireInvocation(invocation);
            switch (method.getName()) {
                case "getConnection" -> { return connection; }
                case "unwrap" -> { return unwrapTracked(proxy, (Class<?>) args[0]); }
                case "isWrapperFor" -> { return ((Class<?>) args[0]).isInstance(proxy); }
                default -> { }
            }
            rejectTransactionSql(method, args);
            if (invocation == null || !method.getName().startsWith("execute")) {
                Object value = call(statement, method, args);
                return value instanceof ResultSet rows
                        ? trackRows(rows, (Statement) proxy, invocation, cancel) : value;
            }
            try {
                if (!peer.statementStarted(invocation, cancel)) {
                    throw new WeavegateCancelledException("cancelled before statement");
                }
                Object value = call(statement, method, args);
                return value instanceof ResultSet rows
                        ? trackRows(rows, (Statement) proxy, invocation, cancel) : value;
            } finally {
                peer.statementFinished(invocation, cancel);
            }
        }
    }

    private Object call(Object target, Method method, Object[] args) throws Throwable {
        try {
            return method.invoke(target, args);
        } catch (InvocationTargetException e) {
            if (e.getCause() instanceof SQLException sql) {
                observeDriverFailure(sql);
            }
            throw e.getCause();
        }
    }

    private void observeDriverFailure(SQLException failure) {
        Peer.Invocation invocation = Peer.current();
        if (invocation != null) {
            peer.driverFailure(invocation, failure, driverSummary(failure));
        }
    }

    private void requireInvocation(Peer.Invocation invocation) {
        if (invocation != null) {
            peer.jdbcEntry(invocation);
        }
    }

    private static void rejectTransactionSql(Method method, Object[] args) throws SQLException {
        if (args == null || args.length == 0 || !(args[0] instanceof String sql)) {
            return;
        }
        String name = method.getName();
        if ((name.startsWith("execute") || name.equals("addBatch") || name.startsWith("prepare"))
                && transactionSql(sql)) {
            throw new SQLFeatureNotSupportedException("application-managed transaction control is unsupported");
        }
    }

    private static boolean transactionSql(String sql) {
        String remaining = sql;
        while (true) {
            remaining = remaining.stripLeading();
            if (remaining.startsWith("--") || remaining.startsWith("#")) {
                int line = lineEnd(remaining);
                if (line == remaining.length()) {
                    return false;
                }
                remaining = remaining.substring(line + 1);
                continue;
            }
            if (!remaining.startsWith("/*")) {
                return TRANSACTION_SQL.matcher(remaining).find();
            }
            int end = remaining.indexOf("*/", 2);
            if (end < 0) {
                return false;
            }
            if (remaining.startsWith("/*!")) {
                String executable = remaining.substring(3, end).stripLeading().replaceFirst("^\\d{5,6}\\s*", "");
                if (transactionSql(executable)) {
                    return true;
                }
            }
            remaining = remaining.substring(end + 2);
        }
    }

    private static int lineEnd(String value) {
        int newline = value.indexOf('\n');
        int carriage = value.indexOf('\r');
        if (newline < 0) {
            return carriage < 0 ? value.length() : carriage;
        }
        return carriage < 0 ? newline : Math.min(newline, carriage);
    }

    private static boolean transactionControl(String method) {
        return method.equals("setAutoCommit") || method.equals("commit") || method.equals("rollback")
                || method.equals("setSavepoint") || method.equals("releaseSavepoint");
    }

    private static boolean transactionControlAllowed() {
        return TRANSACTION_CONTROL.get() != null;
    }

    static void beginTransactionControl() {
        Integer depth = TRANSACTION_CONTROL.get();
        TRANSACTION_CONTROL.set(depth == null ? 1 : depth + 1);
    }

    static void endTransactionControl() {
        Integer depth = TRANSACTION_CONTROL.get();
        if (depth == null || depth == 1) {
            TRANSACTION_CONTROL.remove();
        } else {
            TRANSACTION_CONTROL.set(depth - 1);
        }
    }

    /** Convert an escaping driver exception to fixed wire-safe evidence. */
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
