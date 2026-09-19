package io.github.weavegate.sdk;

import java.io.BufferedInputStream;
import java.io.BufferedOutputStream;
import java.io.FileDescriptor;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.util.concurrent.locks.LockSupport;

/**
 * Opt-in entrypoint for a weavegate child JVM. A test application calls this
 * from the {@code main} method of its dedicated instrumented JAR; ordinary
 * {@code SpringApplication.run} leaves instrumentation inactive.
 *
 * <p>Stdout is reserved for protocol frames before Spring Boot starts and
 * {@code System.out} is redirected to stderr, where all logs belong. The start
 * frame supplies the fixture DataSource; credentials never come from argv,
 * environment variables or files. The process exits through the protocol:
 * status 0 only after {@code stopped} is written and stdout is closed.
 */
public final class WeavegateChild {
    private WeavegateChild() {
    }

    public static void run(Class<?> application, String... args) {
        SpringHost host = new SpringHost(application, args);
        serve(host, host::bind);
    }

    /** Production process wiring shared by every host. Never returns normally. */
    static void serve(Seams.Host host, java.util.function.Consumer<Peer> bind) {
        FileOutputStream protocol = new FileOutputStream(FileDescriptor.out);
        System.setOut(System.err);
        InputStream control = new BufferedInputStream(new FileInputStream(FileDescriptor.in));
        System.setIn(InputStream.nullInputStream());

        StreamOutput output = new StreamOutput(new BufferedOutputStream(protocol));
        Peer peer = new Peer(host, new SystemSeams.MonotonicClock(), SystemSeams.HALT, output, SystemSeams.THREADS,
                Seams.Activity.NONE);
        bind.accept(peer);
        peer.activate();
        output.start(peer);
        Transport.read(peer, new FrameReader(control));
        while (true) {
            // The peer terminates the process; the reader returning never implies success.
            LockSupport.park();
        }
    }
}
