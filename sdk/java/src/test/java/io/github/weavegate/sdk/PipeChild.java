package io.github.weavegate.sdk;

import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.locks.LockSupport;

/**
 * Child JVM for real pipe tests. It uses the production bootstrap, writer,
 * reader, clock, threads and halt; only the application host is scripted.
 */
public final class PipeChild {
    private PipeChild() {
    }

    public static void main(String[] args) {
        if (args[0].equals("halt_with_blocked_stderr")) {
            haltWithBlockedStderr();
            return;
        }
        Host host = new Host(args[0]);
        WeavegateChild.serve(host, peer -> host.peer = peer);
    }

    private static void haltWithBlockedStderr() {
        System.setErr(new java.io.PrintStream(java.io.OutputStream.nullOutputStream()) {
            @Override
            public void flush() {
                Host.parkForever();
            }
        });
        SystemSeams.HALT.halt(23);
    }

    static final class Host implements Seams.Host {
        private final String scenario;
        volatile Peer peer;

        Host(String scenario) {
            this.scenario = scenario;
        }

        @Override
        public void initialize(Seams.Start start) {
            if (scenario.equals("hang_startup")) {
                parkForever();
            }
        }

        @Override
        public Map<String, Set<String>> validateRegistration(List<String> commands, List<String> points) {
            return commands.stream().collect(java.util.stream.Collectors.toUnmodifiableMap(command -> command,
                    command -> Set.copyOf(points)));
        }

        @Override
        public void probeDatabase() {
        }

        @Override
        public void cancelStartup() {
        }

        @Override
        public void execute(CommandContext context) {
            Peer.Invocation invocation = Peer.current();
            peer.leaseAcquired(invocation);
            peer.transactionBegun(invocation);
            try {
                Weavegate.syncPoint("after_read");
            } catch (WeavegateCancelledException e) {
                if (scenario.equals("hang_cleanup")) {
                    parkForever();
                }
                peer.transactionCompleted(invocation, Peer.Transaction.ROLLED_BACK);
                peer.leaseReturned(invocation);
                throw e;
            }
            peer.transactionCompleted(invocation, Peer.Transaction.COMMITTED);
            peer.leaseReturned(invocation);
        }

        @Override
        public void close() {
            if (scenario.equals("hang_shutdown")) {
                parkForever();
            }
        }

        static void parkForever() {
            while (true) {
                LockSupport.park();
            }
        }
    }
}
