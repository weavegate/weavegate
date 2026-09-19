package io.github.weavegate.sdk;

import java.io.PrintWriter;
import java.lang.reflect.InvocationHandler;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.lang.reflect.Proxy;
import java.sql.CallableStatement;
import java.sql.Connection;
import java.sql.PreparedStatement;
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
        return track(delegate.getConnection());
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
            if (invocation != null && result instanceof Statement statement) {
                Class<?> type = result instanceof CallableStatement ? CallableStatement.class
                        : result instanceof PreparedStatement ? PreparedStatement.class : Statement.class;
                return Proxy.newProxyInstance(Statement.class.getClassLoader(), new Class<?>[] {type},
                        new Executing(statement, invocation));
            }
            return result;
        }
    }

    private final class Executing implements InvocationHandler {
        private final Statement statement;
        private final Peer.Invocation invocation;

        Executing(Statement statement, Peer.Invocation invocation) {
            this.statement = statement;
            this.invocation = invocation;
        }

        @Override
        public Object invoke(Object proxy, Method method, Object[] args) throws Throwable {
            if (!method.getName().startsWith("execute")) {
                return call(statement, method, args);
            }
            Seams.Cancellable cancel = statement::cancel;
            try {
                if (!peer.statementStarted(invocation, cancel)) {
                    throw new WeavegateCancelledException("cancelled before statement");
                }
                return call(statement, method, args);
            } catch (SQLException e) {
                peer.recordSource(invocation, e);
                throw e;
            } finally {
                peer.statementFinished(invocation, cancel);
            }
        }
    }

    private static Object call(Object target, Method method, Object[] args) throws Throwable {
        try {
            return method.invoke(target, args);
        } catch (InvocationTargetException e) {
            throw e.getCause();
        }
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
