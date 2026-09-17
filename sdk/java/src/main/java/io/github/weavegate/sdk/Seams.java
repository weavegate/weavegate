package io.github.weavegate.sdk;

import java.util.List;
import java.util.Map;
import java.util.concurrent.Executor;

/**
 * Seams between the protocol peer and its process. Production binds them to the
 * JVM, pipes and Spring; tests bind controllable clocks, exits and barriers.
 */
final class Seams {
    private Seams() {
    }

    /** Application host: initialization, registration, commands and shutdown. */
    interface Host {
        void initialize(Start start) throws Exception;

        void validateRegistration(List<String> commands, List<String> points) throws Exception;

        void probeDatabase() throws Exception;

        /** Best effort: wakes startup work that can observe cancellation. */
        void cancelStartup();

        /** Runs one registered command through its transactional proxy on the calling thread. */
        void execute(CommandContext context) throws Throwable;

        /** Closes the pool and application; returns only after both are closed. */
        void close() throws Exception;
    }

    /** Validated start body, retained without its serialized frame. */
    record Start(String variant, Map<String, String> params, List<String> commands, List<String> points,
                 int capacity, Database database, int startupMillis, int cancelMillis) {
    }

    /** Fixture application account. The password is never logged or formatted. */
    record Database(String host, int port, String name, String username, String password) {
        @Override
        public String toString() {
            return "Database[redacted]";
        }
    }

    interface Clock {
        long nowMillis();

        Timer schedule(long delayMillis, Runnable task);
    }

    interface Timer {
        void cancel();
    }

    interface Exit {
        void halt(int status);
    }

    /** Non-blocking outbound frame sink with a single ordered writer. */
    interface Output {
        boolean enqueue(byte[] payload);

        /** Writes every queued frame, flushes, closes the stream, then runs the callback. */
        void closeAfterDrain(Runnable closed);

        /** Runs the callback after queued frames are written or the fixed delivery bound elapses. */
        void drainWithin(long boundMillis, Runnable then);
    }

    interface Threads {
        void start(String name, Runnable task);

        Executor workers(int capacity);
    }

    /**
     * Test accounting for runnable threads. Production is a no-op; harnesses use
     * it to wait for event-driven quiescence without sleeps.
     */
    interface Activity {
        Activity NONE = new Activity() {
            @Override
            public void busy() {
            }

            @Override
            public void idle() {
            }
        };

        void busy();

        void idle();
    }

    /** Anything a cancellation request can interrupt, such as an executing JDBC statement. */
    interface Cancellable {
        void cancel() throws Exception;
    }
}
