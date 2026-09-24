package io.github.weavegate.sdk;

import javax.sql.DataSource;

import org.springframework.jdbc.datasource.DataSourceTransactionManager;
import org.springframework.transaction.CannotCreateTransactionException;
import org.springframework.transaction.IllegalTransactionStateException;
import org.springframework.transaction.TransactionDefinition;
import org.springframework.transaction.support.DefaultTransactionStatus;

/**
 * Spring's JDBC transaction manager with the invocation's begin and outcome
 * recorded where they are known. Completion callbacks are not used as terminal
 * evidence: Spring runs them before it returns the connection.
 */
final class WeavegateTransactionManager extends DataSourceTransactionManager {
    private static final long serialVersionUID = 1L;

    private final transient Peer peer;

    WeavegateTransactionManager(DataSource dataSource, Peer peer) {
        super(dataSource);
        this.peer = peer;
        setNestedTransactionAllowed(false);
    }

    @Override
    protected void doBegin(Object transaction, TransactionDefinition definition) {
        Peer.Invocation invocation = Peer.current();
        if (invocation == null) {
            peer.unsupported("transaction", "transaction outside invocation");
            throw new CannotCreateTransactionException("transaction outside weavegate invocation");
        }
        beginControl();
        TrackingDataSource.beginTransactionAttempt(invocation);
        try {
            super.doBegin(transaction, definition);
        } finally {
            TrackingDataSource.endTransactionAttempt();
            endControl();
        }
        peer.transactionBegun(invocation);
    }

    @Override
    protected Object doSuspend(Object transaction) {
        peer.unsupported("transaction", "unsupported transaction propagation");
        throw new IllegalTransactionStateException("weavegate supports one REQUIRED transaction per invocation");
    }

    @Override
    protected void prepareForCommit(DefaultTransactionStatus status) {
        FailureObserver.observeSynchronizations();
        super.prepareForCommit(status);
    }

    @Override
    protected void doCommit(DefaultTransactionStatus status) {
        // Include callbacks registered during beforeCommit/beforeCompletion.
        FailureObserver.observeSynchronizations();
        complete(status, true);
    }

    @Override
    protected void doRollback(DefaultTransactionStatus status) {
        complete(status, false);
    }

    private void complete(DefaultTransactionStatus status, boolean commit) {
        Peer.Invocation invocation = Peer.current();
        try {
            beginControl();
            try {
                if (commit) {
                    super.doCommit(status);
                } else {
                    super.doRollback(status);
                }
            } finally {
                endControl();
            }
        } catch (RuntimeException | Error e) {
            if (invocation != null) {
                peer.transactionCompleted(invocation, Peer.Transaction.UNKNOWN);
            }
            throw e;
        }
        if (invocation != null) {
            peer.transactionCompleted(invocation, commit ? Peer.Transaction.COMMITTED : Peer.Transaction.ROLLED_BACK);
        }
    }

    @Override
    protected void doCleanupAfterCompletion(Object transaction) {
        beginControl();
        try {
            super.doCleanupAfterCompletion(transaction);
        } finally {
            endControl();
        }
    }

    private static void beginControl() {
        TrackingDataSource.beginTransactionControl();
    }

    private static void endControl() {
        TrackingDataSource.endTransactionControl();
    }
}
