package io.github.weavegate.sdk;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import java.sql.Connection;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.stream.Stream;
import javax.sql.DataSource;

import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;

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
}
