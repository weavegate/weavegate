package io.github.weavegate.sdk;

import java.io.PrintWriter;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Proxy;
import java.sql.Connection;
import java.sql.SQLException;
import java.sql.SQLFeatureNotSupportedException;
import java.util.logging.Logger;

import javax.sql.DataSource;

import io.github.weavegate.sdk.fixture.Journal;

/**
 * Driver-level failure injection beneath the SDK's lease tracking. Faults are
 * applied after the real driver call where needed, so pooled connections are
 * still physically returned; the SDK only sees the exception Spring suppresses.
 */
final class FaultyDataSource implements DataSource {
    enum Fault { NONE, BEGIN, COMMIT, ROLLBACK, CLOSE }

    private final DataSource delegate;
    volatile Fault fault = Fault.NONE;
    /** Counts down when a locking read reaches the driver, beneath the SDK's statement registration. */
    volatile java.util.concurrent.CountDownLatch lockingRead = new java.util.concurrent.CountDownLatch(0);

    FaultyDataSource(DataSource delegate) {
        this.delegate = delegate;
    }

    @Override
    public Connection getConnection() throws SQLException {
        Connection connection = delegate.getConnection();
        return (Connection) Proxy.newProxyInstance(Connection.class.getClassLoader(), new Class<?>[] {Connection.class},
                (proxy, method, args) -> {
                    String name = method.getName();
                    if (name.equals("setAutoCommit") && Boolean.FALSE.equals(args[0]) && fault == Fault.BEGIN) {
                        throw new SQLException("synthetic begin failure");
                    }
                    if (name.equals("commit") && fault == Fault.COMMIT) {
                        connection.rollback();
                        throw new SQLException("synthetic commit failure");
                    }
                    if (name.equals("rollback") && args == null && fault == Fault.ROLLBACK) {
                        connection.rollback();
                        throw new SQLException("synthetic rollback failure");
                    }
                    Object result;
                    try {
                        result = method.invoke(connection, args);
                    } catch (InvocationTargetException e) {
                        throw e.getCause();
                    }
                    if (result instanceof java.sql.Statement statement) {
                        String prepared = args != null && args.length > 0 && args[0] instanceof String sql ? sql : "";
                        Class<?> type = statement instanceof java.sql.PreparedStatement
                                ? java.sql.PreparedStatement.class : java.sql.Statement.class;
                        return Proxy.newProxyInstance(Connection.class.getClassLoader(), new Class<?>[] {type}, (p, m, a) -> {
                            String executed = a != null && a.length > 0 && a[0] instanceof String sql ? sql : prepared;
                            if (m.getName().startsWith("execute") && executed.contains("FOR UPDATE")) {
                                lockingRead.countDown();
                            }
                            try {
                                return m.invoke(statement, a);
                            } catch (InvocationTargetException e) {
                                throw e.getCause();
                            }
                        });
                    }
                    if (name.equals("commit") || (name.equals("rollback") && args == null) || name.equals("close")) {
                        Journal.add("driver-" + name);
                    }
                    if (name.equals("close") && fault == Fault.CLOSE) {
                        throw new SQLException("synthetic close failure");
                    }
                    return result;
                });
    }

    @Override
    public Connection getConnection(String username, String password) throws SQLException {
        throw new SQLFeatureNotSupportedException();
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
        throw new SQLException("not a wrapper");
    }

    @Override
    public boolean isWrapperFor(Class<?> type) {
        return false;
    }
}
