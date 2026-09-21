package io.github.weavegate.sdk;

import java.util.concurrent.Executor;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

/** Production clock, exit and thread seams. */
final class SystemSeams {
    private SystemSeams() {
    }

    static final class MonotonicClock implements Seams.Clock {
        private final long origin = System.nanoTime();
        private final ScheduledExecutorService watchdogs = Executors.newSingleThreadScheduledExecutor(task -> {
            Thread thread = new Thread(task, "weavegate-watchdog");
            thread.setDaemon(true);
            return thread;
        });

        @Override
        public long nowMillis() {
            return TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - origin);
        }

        @Override
        public Seams.Timer schedule(long delayMillis, Runnable task) {
            ScheduledFuture<?> future = watchdogs.schedule(task, delayMillis, TimeUnit.MILLISECONDS);
            return () -> future.cancel(false);
        }
    }

    /** Forced termination skips shutdown hooks, so a hung pool or driver cannot delay exit. */
    static final Seams.Exit HALT = status -> java.lang.Runtime.getRuntime().halt(status);

    static final Seams.Threads THREADS = new Seams.Threads() {
        @Override
        public void start(String name, Runnable task) {
            Thread thread = new Thread(task, name);
            thread.setDaemon(true);
            thread.start();
        }

        @Override
        public Executor workers(int capacity) {
            AtomicInteger index = new AtomicInteger();
            return Executors.newFixedThreadPool(capacity, task -> {
                Thread thread = new Thread(task, "weavegate-worker-" + index.incrementAndGet());
                thread.setDaemon(true);
                return thread;
            });
        }
    };
}
