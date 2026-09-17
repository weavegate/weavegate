package io.github.weavegate.sdk;

import static org.assertj.core.api.Assertions.assertThat;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.stream.Stream;

import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;
import tools.jackson.databind.JsonNode;

/**
 * Owned child JVMs over real OS pipes. The parent observes frames, stdout EOF
 * and process exit independently; bounds only detect a hung test.
 */
class RealPipesTest {
    private static final String HANDLER = "sdk/java/src/test/java/io/github/weavegate/sdk/RealPipesTest.java:RealPipesTest.";
    private static final long BOUND_SECONDS = 30;

    /** Parent side of one child: stdout frames are decoded on a reader thread. */
    static final class Child implements AutoCloseable {
        final Process process;
        final OutputStream stdin;
        final BlockingQueue<Object> frames = new LinkedBlockingQueue<>();
        int seq;

        Child(String scenario, boolean readStdout) throws IOException {
            String java = Path.of(System.getProperty("java.home"), "bin", "java").toString();
            process = new ProcessBuilder(java, "-cp", System.getProperty("java.class.path"), PipeChild.class.getName(), scenario)
                    .redirectError(ProcessBuilder.Redirect.DISCARD).start();
            stdin = process.getOutputStream();
            if (readStdout) {
                Thread reader = new Thread(() -> {
                    FrameReader frameReader = new FrameReader(process.getInputStream());
                    try {
                        while (true) {
                            byte[] payload = frameReader.next();
                            if (payload == null) {
                                frames.add("EOF");
                                return;
                            }
                            Wire.decode(payload);
                            frames.add(Vectors.JSON.readTree(payload));
                        }
                    } catch (Exception e) {
                        frames.add(e);
                    }
                }, "pipe-test-reader");
                reader.setDaemon(true);
                reader.start();
            }
        }

        void send(byte[] payload) throws IOException {
            int n = payload.length;
            stdin.write(new byte[] {(byte) (n >>> 24), (byte) (n >>> 16), (byte) (n >>> 8), (byte) n});
            stdin.write(payload);
            stdin.flush();
        }

        JsonNode expect(String type) throws InterruptedException {
            Object item = frames.poll(BOUND_SECONDS, TimeUnit.SECONDS);
            assertThat(item).as("frame " + type).isInstanceOf(JsonNode.class);
            JsonNode frame = (JsonNode) item;
            assertThat(frame.get("type").stringValue()).isEqualTo(type);
            assertThat(frame.get("seq").intValue()).isEqualTo(++seq);
            return frame;
        }

        void expectEof() throws InterruptedException {
            assertThat(frames.poll(BOUND_SECONDS, TimeUnit.SECONDS)).isEqualTo("EOF");
        }

        int exit() throws InterruptedException {
            assertThat(process.waitFor(BOUND_SECONDS, TimeUnit.SECONDS)).as("child exited").isTrue();
            return process.exitValue();
        }

        void ready() throws Exception {
            send(Scripted.frame("start", 1, Map.of("variant", "fixed", "params", Map.of(),
                    "commands", List.of("assign"), "points", List.of("after_read"), "capacity", 1,
                    "database", Map.of("driver", "mysql", "host", "127.0.0.1", "port", 3306, "name", "weavegate",
                            "username", "synthetic", "password", "synthetic-only"),
                    "startup_ms", 60000, "cancel_ms", 500)));
            expect("ready");
        }

        void arrived() throws Exception {
            send(Scripted.invoke(2, Scripted.I1, "w1", "assign"));
            expect("accepted");
            expect("arrive");
        }

        @Override
        public void close() {
            process.destroyForcibly();
        }
    }

    @TestFactory
    Stream<DynamicTest> successLifecycleOverRealPipes() {
        return RequirementsTest.repeated(() -> {
            try (Child child = new Child("normal", true)) {
                child.ready();
                child.arrived();
                child.send(Scripted.release(3, Scripted.I1, "w1", "1", "after_read"));
                JsonNode terminal = child.expect("terminal");
                assertThat(terminal.get("body").get("transaction").stringValue()).isEqualTo("committed");
                assertThat(terminal.get("body").get("error").isNull()).isTrue();
                child.send(Scripted.stop(4, 5000));
                child.expect("stopped");
                child.expectEof();
                assertThat(child.exit()).isZero();
            }
            EvidenceListener.check("requirement/java-success-lifecycle", "observe/evidence", HANDLER + "successLifecycleOverRealPipes");
        });
    }

    @TestFactory
    Stream<DynamicTest> faultsOverRealPipes() {
        return RequirementsTest.repeated(() -> {
            List<String> observed = new ArrayList<>();

            // EOF before start: no identity or cancellation bound exists.
            try (Child child = new Child("normal", true)) {
                child.stdin.close();
                JsonNode fatal = child.expect("fatal");
                assertThat(fatal.get("body").get("kind").stringValue()).isEqualTo("transport");
                child.expectEof();
                assertThat(child.exit()).isEqualTo(1);
                observed.add("eof_before_start");
            }

            // EOF during hung startup: the cleanup watchdog forces exit without readiness.
            try (Child child = new Child("hang_startup", true)) {
                child.send(Scripted.frame("start", 1, Map.of("variant", "fixed", "params", Map.of(),
                        "commands", List.of("assign"), "points", List.of("after_read"), "capacity", 1,
                        "database", Map.of("driver", "mysql", "host", "127.0.0.1", "port", 3306, "name", "weavegate",
                                "username", "synthetic", "password", "synthetic-only"),
                        "startup_ms", 60000, "cancel_ms", 500)));
                child.stdin.close();
                assertThat(child.expect("fatal").get("body").get("kind").stringValue()).isEqualTo("transport");
                assertThat(child.exit()).isEqualTo(1);
                observed.add("eof_startup");
            }

            // EOF with an outstanding arrival: the gate wakes, rollback completes, no terminal is sent.
            try (Child child = new Child("normal", true)) {
                child.ready();
                child.arrived();
                child.stdin.close();
                assertThat(child.expect("fatal").get("body").get("kind").stringValue()).isEqualTo("transport");
                child.expectEof();
                assertThat(child.exit()).isEqualTo(1);
                observed.add("eof_active");
            }

            // EOF with cleanup that never completes: the watchdog forces exit.
            try (Child child = new Child("hang_cleanup", true)) {
                child.ready();
                child.arrived();
                child.stdin.close();
                assertThat(child.expect("fatal").get("body").get("kind").stringValue()).isEqualTo("transport");
                assertThat(child.exit()).isEqualTo(1);
                observed.add("eof_active_watchdog");
            }

            // EOF after the terminal but before Stop.
            try (Child child = new Child("normal", true)) {
                child.ready();
                child.arrived();
                child.send(Scripted.release(3, Scripted.I1, "w1", "1", "after_read"));
                child.expect("terminal");
                child.stdin.close();
                assertThat(child.expect("fatal").get("body").get("kind").stringValue()).isEqualTo("transport");
                child.expectEof();
                assertThat(child.exit()).isEqualTo(1);
                observed.add("eof_post_terminal");
            }

            // EOF during a Stop whose application shutdown hangs: the stop watchdog forces exit.
            try (Child child = new Child("hang_shutdown", true)) {
                child.ready();
                child.send(Scripted.stop(2, 1000));
                child.stdin.close();
                assertThat(child.expect("fatal").get("body").get("kind").stringValue()).isEqualTo("transport");
                assertThat(child.exit()).isEqualTo(1);
                observed.add("eof_stop");
            }

            // Broken writer: the parent closes the child's stdout before the next frame.
            try (Child child = new Child("normal", false)) {
                FrameReader reader = new FrameReader(child.process.getInputStream());
                child.send(Scripted.frame("start", 1, Map.of("variant", "fixed", "params", Map.of(),
                        "commands", List.of("assign"), "points", List.of("after_read"), "capacity", 1,
                        "database", Map.of("driver", "mysql", "host", "127.0.0.1", "port", 3306, "name", "weavegate",
                                "username", "synthetic", "password", "synthetic-only"),
                        "startup_ms", 60000, "cancel_ms", 500)));
                assertThat(Wire.decode(reader.next()).type()).isEqualTo("ready");
                child.process.getInputStream().close();
                child.send(Scripted.invoke(2, Scripted.I1, "w1", "assign"));
                assertThat(child.exit()).isEqualTo(1);
                observed.add("broken_writer");
            }

            // Blocked writer: an unread ready frame larger than the pipe buffer; Stop's watchdog still exits.
            try (Child child = new Child("normal", false)) {
                List<String> commands = new ArrayList<>();
                for (int i = 0; i < 6000; i++) {
                    commands.add(String.format("command_%0120d", i));
                }
                child.send(Scripted.frame("start", 1, Map.of("variant", "fixed", "params", Map.of(),
                        "commands", commands, "points", List.of(), "capacity", 1,
                        "database", Map.of("driver", "mysql", "host", "127.0.0.1", "port", 3306, "name", "weavegate",
                                "username", "synthetic", "password", "synthetic-only"),
                        "startup_ms", 60000, "cancel_ms", 500)));
                // Reading only the length header proves ready is in the writer; its payload exceeds the pipe buffer.
                InputStream unread = child.process.getInputStream();
                byte[] header = unread.readNBytes(4);
                long length = ((header[0] & 0xffL) << 24) | ((header[1] & 0xffL) << 16) | ((header[2] & 0xffL) << 8) | (header[3] & 0xffL);
                assertThat(length).isGreaterThan(1L << 19);
                child.send(Scripted.stop(2, 1000));
                assertThat(child.exit()).isEqualTo(1);
                assertThat(unread.available()).isPositive();
                observed.add("blocked_writer");
            }
            assertThat(observed).hasSize(8);
            EvidenceListener.check("requirement/java-real-pipes", "observe/evidence", HANDLER + "faultsOverRealPipes");
        });
    }

    @AfterAll
    static void markers() {
        EvidenceListener.marker("EXTERNAL_SUT_JAVA_PROCESS_RESULT pipes=real success=stopped_eof_exit0 eof=startup,active,post_terminal,stop "
                + "writer=broken,blocked watchdog=self_exit");
    }
}
