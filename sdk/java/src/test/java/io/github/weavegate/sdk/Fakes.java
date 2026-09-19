package io.github.weavegate.sdk;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Queue;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.Executor;
import java.util.concurrent.TimeUnit;

import tools.jackson.databind.JsonNode;

/** Controllable seams for isolated peer tests. None of them decides protocol outcomes. */
final class Fakes {
    private Fakes() {
    }

    /** Counts runnable harness-owned threads; waiting uses a monitor, never sleep. */
    static final class Activity implements Seams.Activity {
        private int busy;
        private final List<Throwable> failures = new CopyOnWriteArrayList<>();

        @Override
        public synchronized void busy() {
            busy++;
        }

        @Override
        public synchronized void idle() {
            busy--;
            notifyAll();
        }

        synchronized void awaitIdle() {
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(20);
            while (busy > 0) {
                long remaining = TimeUnit.NANOSECONDS.toMillis(deadline - System.nanoTime());
                if (remaining <= 0) {
                    throw new AssertionError("harness threads did not quiesce");
                }
                try {
                    wait(remaining);
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    throw new AssertionError(e);
                }
            }
            if (busy < 0) {
                throw new AssertionError("activity accounting underflow");
            }
            if (!failures.isEmpty()) {
                throw new AssertionError("harness thread failed", failures.getFirst());
            }
        }

        void failed(Throwable t) {
            failures.add(t);
        }
    }

    /** Fake monotonic clock. Advancing runs already-armed callbacks on the caller's thread. */
    static final class Clock implements Seams.Clock {
        final class Timer implements Seams.Timer {
            final long deadline;
            final Runnable task;
            boolean cancelled;
            boolean fired;

            Timer(long deadline, Runnable task) {
                this.deadline = deadline;
                this.task = task;
            }

            @Override
            public void cancel() {
                synchronized (Clock.this) {
                    cancelled = true;
                }
            }

            boolean pending() {
                synchronized (Clock.this) {
                    return !cancelled && !fired;
                }
            }
        }

        private long now;
        private final List<Timer> timers = new ArrayList<>();

        @Override
        public synchronized long nowMillis() {
            return now;
        }

        @Override
        public synchronized Seams.Timer schedule(long delayMillis, Runnable task) {
            Timer timer = new Timer(now + delayMillis, task);
            timers.add(timer);
            return timer;
        }

        void advanceTo(long time) {
            List<Timer> due = new ArrayList<>();
            synchronized (this) {
                if (time < now) {
                    throw new AssertionError("fake clock cannot move backwards");
                }
                now = time;
                for (Timer timer : timers) {
                    if (!timer.cancelled && !timer.fired && timer.deadline <= time) {
                        timer.fired = true;
                        due.add(timer);
                    }
                }
            }
            due.sort(Comparator.comparingLong(t -> t.deadline));
            due.forEach(t -> t.task.run());
        }
    }

    static class Exit implements Seams.Exit {
        Integer status;
        int calls;

        @Override
        public synchronized void halt(int code) {
            calls++;
            if (status == null) {
                status = code;
            }
        }

        synchronized Integer status() {
            return status;
        }
    }

    /** Captures ordered frames synchronously and validates each against the strict codec. */
    static class Output implements Seams.Output {
        final List<JsonNode> frames = new CopyOnWriteArrayList<>();
        volatile boolean closed;

        @Override
        public boolean enqueue(byte[] payload) {
            if (closed) {
                return false;
            }
            try {
                Wire.decode(payload);
            } catch (Wire.WireException e) {
                throw new AssertionError("peer emitted an invalid frame: " + e.getMessage());
            }
            frames.add(Vectors.JSON.readTree(payload));
            return true;
        }

        @Override
        public void closeAfterDrain(Runnable closedCallback) {
            closed = true;
            closedCallback.run();
        }

        @Override
        public void drainWithin(long boundMillis, Runnable then) {
            then.run();
        }
    }

    /** Starts peer threads with activity accounting; holds worker tasks until the harness runs them. */
    static final class Threads implements Seams.Threads {
        private final Activity activity;
        final Map<String, List<Peer.InvocationTask>> submitted = new ConcurrentHashMap<>();
        final Map<String, Boolean> started = new ConcurrentHashMap<>();
        final List<Thread> threads = new CopyOnWriteArrayList<>();
        int capacity;

        Threads(Activity activity) {
            this.activity = activity;
        }

        @Override
        public void start(String name, Runnable task) {
            activity.busy();
            Thread thread = new Thread(() -> {
                try {
                    task.run();
                } catch (Throwable t) {
                    activity.failed(t);
                } finally {
                    activity.idle();
                }
            }, name);
            thread.setDaemon(true);
            threads.add(thread);
            thread.start();
        }

        @Override
        public Executor workers(int capacity) {
            this.capacity = capacity;
            return task -> {
                Peer.InvocationTask invocationTask = (Peer.InvocationTask) task;
                submitted.computeIfAbsent(invocationTask.invocation.id, k -> new CopyOnWriteArrayList<>()).add(invocationTask);
            };
        }

        int submissions(String invocation) {
            return submitted.getOrDefault(invocation, List.of()).size();
        }

        /** Runs the held task once, as a worker thread picking it up. */
        void run(String invocation) {
            List<Peer.InvocationTask> tasks = submitted.get(invocation);
            if (tasks == null || tasks.size() != 1) {
                throw new AssertionError("no single held task for invocation");
            }
            if (started.putIfAbsent(invocation, true) == null) {
                start("weavegate-worker-" + invocation, tasks.getFirst());
            }
        }

        void interruptAll() {
            threads.forEach(Thread::interrupt);
        }
    }

    /** Wait point for a harness-controlled barrier with activity accounting. */
    static final class Barrier extends Parker {
        Barrier(Seams.Activity activity) {
            super(activity);
        }
    }

    /** Instructions for a scripted command; one mailbox per invocation. */
    static final class Mailbox {
        private final Seams.Activity activity;
        private final Queue<Object> items = new ArrayDeque<>();
        private boolean waiting;

        Mailbox(Seams.Activity activity) {
            this.activity = activity;
        }

        synchronized Object take() throws InterruptedException {
            while (items.isEmpty()) {
                if (!waiting) {
                    waiting = true;
                    activity.idle();
                }
                wait();
            }
            return items.remove();
        }

        synchronized void put(Object item) {
            if (waiting) {
                waiting = false;
                activity.busy();
            }
            items.add(item);
            notifyAll();
        }
    }

    record Arrive(String point) {
    }

    record Fail(Throwable exception) {
    }

    record BlockJdbc() {
    }

    record ExitProxy() {
    }

    /** Scripted command state observed by assertions. */
    static final class Script {
        final Mailbox mailbox;
        final List<String> resumed = Collections.synchronizedList(new ArrayList<>());
        volatile WeavegateCancelledException cancelObserved;
        volatile Throwable pending;
        volatile boolean blockedInJdbc;
        volatile boolean jdbcResumed;
        volatile boolean returned;

        Script(Seams.Activity activity) {
            mailbox = new Mailbox(activity);
        }
    }

    /** Application host with independently controlled initialization, probe, JDBC and shutdown barriers. */
    static final class Host implements Seams.Host {
        final Activity activity;
        Peer peer;
        final Map<String, Script> scripts = new HashMap<>();
        volatile int initializeCalls;
        volatile boolean initializationStarted;
        volatile boolean initializationReturned;
        volatile boolean holdInitialization;
        final Barrier initialization;
        volatile boolean validated;
        volatile boolean probeStarted;
        volatile boolean probeLeaseReturned;
        final Barrier probe;
        volatile int cancelStartupCalls;
        volatile boolean closeStarted;
        volatile boolean closeCompleted;
        volatile boolean holdShutdown;
        final Barrier shutdown;
        volatile boolean holdRollback;
        final Barrier databaseLock;

        Host(Activity activity) {
            this.activity = activity;
            initialization = new Barrier(activity);
            probe = new Barrier(activity);
            shutdown = new Barrier(activity);
            databaseLock = new Barrier(activity);
        }

        synchronized Script script(String invocation) {
            return scripts.computeIfAbsent(invocation, k -> new Script(activity));
        }

        @Override
        public void initialize(Seams.Start start) throws Exception {
            initializeCalls++;
            initializationStarted = true;
            if (holdInitialization) {
                initialization.await();
            }
            initializationReturned = true;
        }

        @Override
        public void validateRegistration(List<String> commands, List<String> points) {
            if (!List.of("assign").containsAll(commands) || !List.of("after_read", "before_write").containsAll(points)) {
                throw new IllegalStateException("unsupported registration");
            }
            validated = true;
        }

        @Override
        public void probeDatabase() throws Exception {
            probeStarted = true;
            peer.leaseAcquired(null);
            probe.await();
            if (cancelStartupCalls > 0 && !probeLeaseReturned) {
                peer.leaseReturned(null);
                throw new IllegalStateException("startup cancelled");
            }
            peer.leaseReturned(null);
        }

        void completeProbe() {
            probeLeaseReturned = true;
            probe.signal();
        }

        @Override
        public void cancelStartup() {
            cancelStartupCalls++;
            probe.signal();
        }

        @Override
        public void execute(CommandContext context) throws Throwable {
            Script script = script(context.invocation());
            while (true) {
                Object instruction = script.mailbox.take();
                switch (instruction) {
                    case Arrive arrive -> {
                        try {
                            Weavegate.syncPoint(arrive.point());
                            script.resumed.add(arrive.point());
                        } catch (WeavegateCancelledException e) {
                            script.cancelObserved = e;
                            script.pending = e;
                        }
                    }
                    case Fail fail -> {
                        // The production hook records the source where the failure is observed.
                        peer.recordSource(Peer.current(), fail.exception());
                        script.pending = fail.exception();
                    }
                    case BlockJdbc ignored -> {
                        Peer.Invocation invocation = Peer.current();
                        Seams.Cancellable statement = () -> {
                        };
                        peer.statementStarted(invocation, statement);
                        script.blockedInJdbc = true;
                        databaseLock.await();
                        script.blockedInJdbc = false;
                        script.jdbcResumed = true;
                        peer.statementFinished(invocation, statement);
                    }
                    case ExitProxy ignored -> {
                        script.returned = true;
                        if (script.pending != null) {
                            throw script.pending;
                        }
                        return;
                    }
                    default -> throw new AssertionError("unknown instruction");
                }
            }
        }

        @Override
        public void close() throws Exception {
            closeStarted = true;
            shutdown.await();
            closeCompleted = true;
        }
    }
}
