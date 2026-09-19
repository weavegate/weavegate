package io.github.weavegate.sdk;

/**
 * Explicit sync-point API for instrumented application code.
 *
 * <p>Instrumentation is inactive unless the application was launched through
 * {@link WeavegateChild#run}. Inactive calls return immediately: they read no
 * input, start no protocol loop and do not touch the current transaction.
 */
public final class Weavegate {
    private Weavegate() {
    }

    /**
     * Blocks the current worker at {@code point} until the engine releases this
     * exact arrival. Throws {@link WeavegateCancelledException} if the invocation
     * is canceled or the session fails first. There is no timeout or sleep.
     */
    public static void syncPoint(String point) {
        Peer.Invocation invocation = Peer.current();
        if (invocation != null) {
            invocation.peer().arrive(invocation, point);
            return;
        }
        Peer active = Peer.activeSession();
        if (active != null) {
            active.outsideInvocation();
        }
    }

    /** Returns whether this JVM was launched as an enabled weavegate child. */
    public static boolean active() {
        return Peer.activeSession() != null;
    }
}
