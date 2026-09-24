package io.github.weavegate.sdk;

import java.io.IOException;
import java.io.OutputStream;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

/**
 * Single ordered writer. Enqueueing never blocks: a full queue fails the
 * session instead of holding the peer's serialization point during pipe I/O.
 */
final class StreamOutput implements Seams.Output {
    static final int QUEUE_CAPACITY = 4096;

    private final OutputStream out;
    private final BlockingQueue<Object> queue = new ArrayBlockingQueue<>(QUEUE_CAPACITY);
    private final Thread writer;
    private volatile Peer peer;
    private volatile boolean failed;

    private record Marker(Runnable action, CountDownLatch done) {
    }

    StreamOutput(OutputStream out) {
        this.out = out;
        this.writer = new Thread(this::write, "weavegate-writer");
        this.writer.setDaemon(true);
    }

    void start(Peer owner) {
        this.peer = owner;
        writer.start();
    }

    @Override
    public boolean enqueue(byte[] payload) {
        return !failed && queue.offer(payload);
    }

    @Override
    public void closeAfterDrain(Runnable closed) {
        if (!queue.offer(new Marker(() -> {
            if (!failed) {
                try {
                    out.close();
                } catch (IOException e) {
                    writeFailed();
                }
            }
            closed.run();
        }, new CountDownLatch(1)))) {
            writeFailed();
            closed.run();
        }
    }

    @Override
    public void drainWithin(long boundMillis, Runnable then) {
        Marker marker = new Marker(() -> {
        }, new CountDownLatch(1));
        try {
            if (queue.offer(marker)) {
                marker.done().await(boundMillis, TimeUnit.MILLISECONDS);
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        then.run();
    }

    private void write() {
        try {
            while (true) {
                Object item = queue.take();
                if (item instanceof Marker marker) {
                    if (!failed) {
                        try {
                            out.flush();
                        } catch (IOException e) {
                            writeFailed();
                        }
                    }
                    marker.action().run();
                    marker.done().countDown();
                    continue;
                }
                if (failed) {
                    continue;
                }
                byte[] payload = (byte[]) item;
                int n = payload.length;
                out.write(new byte[] {(byte) (n >>> 24), (byte) (n >>> 16), (byte) (n >>> 8), (byte) n});
                out.write(payload);
                out.flush();
            }
        } catch (IOException e) {
            writeFailed();
            drainAfterFailure();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    private void writeFailed() {
        failed = true;
        Peer owner = peer;
        if (owner != null) {
            owner.writeFailed();
        }
    }

    private void drainAfterFailure() {
        try {
            while (true) {
                if (queue.take() instanceof Marker marker) {
                    marker.action().run();
                    marker.done().countDown();
                }
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
