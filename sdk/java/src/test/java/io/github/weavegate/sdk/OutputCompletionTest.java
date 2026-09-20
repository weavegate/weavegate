package io.github.weavegate.sdk;

import static org.assertj.core.api.Assertions.assertThat;

import java.io.IOException;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.stream.Stream;

import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;

class OutputCompletionTest {
    record Rig(Fakes.Activity activity, Fakes.Host host, Peer peer, Fakes.Clock clock,
               SpringTransactionsTest.Exit exit) {
        static Rig ready(Seams.Output output) {
            Fakes.Activity activity = new Fakes.Activity();
            Fakes.Host host = new Fakes.Host(activity);
            Fakes.Clock clock = new Fakes.Clock();
            SpringTransactionsTest.Exit exit = new SpringTransactionsTest.Exit();
            Peer peer = new Peer(host, clock, exit, output, new Fakes.Threads(activity), activity);
            host.peer = peer;
            host.shutdown.signal();
            peer.receive(Scripted.start(1));
            activity.awaitIdle();
            host.completeProbe();
            activity.awaitIdle();
            return new Rig(activity, host, peer, clock, exit);
        }

        void stop() {
            peer.receive(Scripted.stop(2, 2500));
            activity.awaitIdle();
        }
    }

    @TestFactory
    Stream<DynamicTest> stoppedRemainsSupervisedUntilDrained() {
        return RequirementsTest.repeated(() -> {
            class Held extends Fakes.Output {
                Runnable completion;
                @Override
                public void closeAfterDrain(Runnable callback) {
                    completion = callback;
                }
            }
            Held output = new Held();
            Rig rig = Rig.ready(output);
            rig.stop();
            assertThat(rig.peer.phase).isEqualTo(Peer.Phase.STOPPING);
            assertThat(rig.exit.status()).isNull();
            rig.peer.writeFailed();
            output.completion.run();
            assertThat(rig.exit.status()).isEqualTo(1);
        });
    }

    @TestFactory
    Stream<DynamicTest> stoppedEnqueueFailureCannotExitZero() {
        return RequirementsTest.repeated(() -> {
            Fakes.Output output = new Fakes.Output();
            Rig rig = Rig.ready(output);
            output.closed = true;
            rig.stop();
            assertThat(rig.exit.status()).isEqualTo(1);
            assertThat(rig.peer.fatalKind).isEqualTo("transport");
        });
    }

    @TestFactory
    Stream<DynamicTest> realWriterFailuresCannotExitZero() {
        return RequirementsTest.repeated(() -> {
            for (String mode : new String[] {"write", "flush", "close", "marker_overflow"}) {
                var writeEntered = new java.util.concurrent.CountDownLatch(1);
                var writeAllowed = new java.util.concurrent.CountDownLatch(1);
                StreamOutput output = new StreamOutput(new OutputStream() {
                    boolean stopped;
                    @Override
                    public void write(int b) { }
                    @Override
                    public void write(byte[] payload) throws IOException {
                        if (mode.equals("marker_overflow")) {
                            writeEntered.countDown();
                            try {
                                if (!writeAllowed.await(30, java.util.concurrent.TimeUnit.SECONDS)) {
                                    throw new IOException("test did not release writer");
                                }
                            } catch (InterruptedException e) {
                                Thread.currentThread().interrupt();
                                throw new IOException(e);
                            }
                        }
                        if (new String(payload, StandardCharsets.UTF_8).contains("\"stopped\"")) {
                            stopped = true;
                            if (mode.equals("write")) {
                                throw new IOException("synthetic broken final write");
                            }
                        }
                    }
                    @Override
                    public void flush() throws IOException {
                        if (stopped && mode.equals("flush")) {
                            throw new IOException("synthetic failed flush");
                        }
                    }
                    @Override
                    public void close() throws IOException {
                        if (mode.equals("close")) {
                            throw new IOException("synthetic failed close");
                        }
                    }
                });
                Rig rig = Rig.ready(output);
                output.start(rig.peer);
                if (mode.equals("marker_overflow")) {
                    assertThat(writeEntered.await(30, java.util.concurrent.TimeUnit.SECONDS)).isTrue();
                    // Leave exactly one slot for stopped, but none for its close marker.
                    for (int n = 0; n < StreamOutput.QUEUE_CAPACITY - 1; n++) {
                        assertThat(output.enqueue(new byte[] {1})).isTrue();
                    }
                }
                try {
                    rig.stop();
                } finally {
                    writeAllowed.countDown();
                }
                assertThat(rig.exit.await()).as(mode).isEqualTo(1);
            }
        });
    }
}
