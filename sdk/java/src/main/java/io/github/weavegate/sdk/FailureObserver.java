package io.github.weavegate.sdk;

import java.lang.reflect.InvocationHandler;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.lang.reflect.Proxy;

import org.aopalliance.intercept.MethodInterceptor;
import org.aopalliance.intercept.MethodInvocation;
import org.springframework.transaction.support.TransactionSynchronization;
import org.springframework.transaction.support.TransactionSynchronizationManager;

/** Observes failure order without deciding rollback, commit, or terminal completion. */
final class FailureObserver implements MethodInterceptor {
    private static final ThreadLocal<Boolean> INSIDE_COMMAND = new ThreadLocal<>();

    @Override
    public Object invoke(MethodInvocation call) throws Throwable {
        Peer.Invocation invocation = Peer.current();
        if (invocation == null || INSIDE_COMMAND.get() != null) {
            return call.proceed();
        }
        INSIDE_COMMAND.set(true);
        try {
            return call.proceed();
        } catch (Throwable failure) {
            invocation.peer().recordSource(invocation, failure);
            throw failure;
        } finally {
            INSIDE_COMMAND.remove();
        }
    }

    /** Preserve callback order and exceptions; callbacks still cannot publish a terminal. */
    static void observeSynchronizations() {
        Peer.Invocation invocation = Peer.current();
        if (invocation == null || !TransactionSynchronizationManager.isSynchronizationActive()) {
            return;
        }
        var callbacks = TransactionSynchronizationManager.getSynchronizations();
        if (callbacks.stream().allMatch(FailureObserver::observed)) {
            return;
        }
        TransactionSynchronizationManager.clearSynchronization();
        TransactionSynchronizationManager.initSynchronization();
        for (TransactionSynchronization callback : callbacks) {
            TransactionSynchronizationManager.registerSynchronization(observed(callback) ? callback
                    : (TransactionSynchronization) Proxy.newProxyInstance(TransactionSynchronization.class.getClassLoader(),
                            new Class<?>[] {TransactionSynchronization.class}, new Callback(callback, invocation)));
        }
    }

    private static boolean observed(TransactionSynchronization callback) {
        return Proxy.isProxyClass(callback.getClass()) && Proxy.getInvocationHandler(callback) instanceof Callback;
    }

    private record Callback(TransactionSynchronization delegate, Peer.Invocation invocation) implements InvocationHandler {
        @Override
        public Object invoke(Object proxy, Method method, Object[] args) throws Throwable {
            if (method.getName().equals("equals")) {
                return proxy == args[0];
            }
            if (method.getName().equals("hashCode")) {
                return System.identityHashCode(proxy);
            }
            try {
                return method.invoke(delegate, args);
            } catch (InvocationTargetException e) {
                // These callbacks propagate to the proxy caller. Spring intentionally
                // suppresses beforeCompletion/afterCompletion errors; keep that behavior.
                if (method.getName().equals("beforeCommit") || method.getName().equals("afterCommit")) {
                    invocation.peer().recordSource(invocation, e.getCause());
                }
                throw e.getCause();
            }
        }
    }
}
