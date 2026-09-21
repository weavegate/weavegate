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
    private static final Pattern NONTRANSACTIONAL_SQL = Pattern.compile("(?is)^(?:"
            + "(?:ALTER|ANALYZE|CACHE|CHECK|CREATE|DROP|FLUSH|GRANT|INSTALL|LOCK|OPTIMIZE|RENAME|REPAIR|"
            + "RESET|REVOKE|TRUNCATE|UNINSTALL|UNLOCK)\\b|"
            + "LOAD\\s+INDEX\\s+INTO\\s+CACHE\\b|SET\\s+PASSWORD\\b|"
            + "CHANGE\\s+(?:MASTER|REPLICATION)\\b|(?:START|STOP)\\s+(?:REPLICA|SLAVE)\\b)");
    private static final ThreadLocal<Integer> TRANSACTION_CONTROL = new ThreadLocal<>();
    private static final ThreadLocal<Peer.Invocation> TRANSACTION_BEGIN = new ThreadLocal<>();
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
            rejectUnsupportedSql(method, args);
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
                        default -> { }
                    }
                    if (invocation == null || cancel == null) {
                        return call(rows, method, args);
                    }
                    try {
                        if (!peer.statementStarted(invocation, cancel) && !method.getName().equals("close")) {
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
            rejectUnsupportedSql(method, args);
            rejectQueryTimeout(method, args);
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
            if (TRANSACTION_BEGIN.get() == invocation) {
                // Begin failures cannot be caught by command code. Preserve their
                // order before Spring returns the lease and cancellation can race.
                peer.recordSource(invocation, failure);
            }
        }
    }

    private void requireInvocation(Peer.Invocation invocation) {
        if (invocation != null) {
            peer.jdbcEntry(invocation);
        }
    }

    private void rejectUnsupportedSql(Method method, Object[] args) throws SQLException {
        if (args == null || args.length == 0 || !(args[0] instanceof String sql)) {
            return;
        }
        String name = method.getName();
        if (name.startsWith("execute") || name.equals("addBatch") || name.startsWith("prepare")) {
            String normalized = normalizeSql(sql).stripLeading();
            if (TRANSACTION_SQL.matcher(normalized).find()) {
                throw new SQLFeatureNotSupportedException("application-managed transaction control is unsupported");
            }
            if (NONTRANSACTIONAL_SQL.matcher(normalized).find()) {
                peer.unsupported("transaction", "nontransactional SQL is unsupported");
                throw new SQLFeatureNotSupportedException("nontransactional SQL is unsupported");
            }
        }
    }

    private static void rejectQueryTimeout(Method method, Object[] args) throws SQLException {
        if (method.getName().equals("setQueryTimeout") && args != null && args.length == 1
                && args[0] instanceof Integer seconds && seconds != 0) {
            throw new SQLFeatureNotSupportedException("wall-clock JDBC query timeouts are unsupported");
        }
    }

    /** Removes MySQL comments outside quoted values; executable comments retain their SQL body. */
    private static String normalizeSql(String sql) {
        StringBuilder normalized = new StringBuilder(sql.length());
        for (int i = 0; i < sql.length();) {
            char current = sql.charAt(i);
            if (current == '\'' || current == '"' || current == '`') {
                i = appendQuoted(sql, i, normalized);
                continue;
            }
            if (current == '#' || (current == '-' && i + 1 < sql.length() && sql.charAt(i + 1) == '-')) {
                int line = i;
                while (line < sql.length() && sql.charAt(line) != '\n' && sql.charAt(line) != '\r') {
                    line++;
                }
                normalized.append(' ');
                i = line;
                continue;
            }
            if (current == '/' && i + 1 < sql.length() && sql.charAt(i + 1) == '*') {
                int end = sql.indexOf("*/", i + 2);
                if (end < 0) {
                    normalized.append(sql, i, sql.length());
                    break;
                }
                if (i + 2 < sql.length() && sql.charAt(i + 2) == '!') {
                    String executable = sql.substring(i + 3, end).stripLeading()
                            .replaceFirst("^\\d{5,6}\\s*", "");
                    normalized.append(' ').append(normalizeSql(executable)).append(' ');
                } else {
                    normalized.append(' ');
                }
                i = end + 2;
                continue;
            }
            normalized.append(current);
            i++;
        }
        return normalized.toString();
    }

    private static int appendQuoted(String sql, int offset, StringBuilder normalized) {
        char quote = sql.charAt(offset);
        normalized.append(quote);
        int i = offset + 1;
        while (i < sql.length()) {
            char current = sql.charAt(i);
            normalized.append(current);
            i++;
            if (current == '\\' && i < sql.length()) {
                normalized.append(sql.charAt(i++));
            } else if (current == quote) {
                if (i < sql.length() && sql.charAt(i) == quote) {
                    normalized.append(sql.charAt(i++));
                } else {
                    break;
                }
            }
        }
        return i;
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

    static void beginTransactionAttempt(Peer.Invocation invocation) {
        TRANSACTION_BEGIN.set(invocation);
    }

    static void endTransactionAttempt() {
        TRANSACTION_BEGIN.remove();
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
