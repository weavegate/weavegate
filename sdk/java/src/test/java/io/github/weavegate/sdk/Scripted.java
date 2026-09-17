package io.github.weavegate.sdk;

import java.util.List;
import java.util.Map;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.node.ObjectNode;

/** Builds engine frames for independently written peer tests. */
final class Scripted {
    static final String RUN = "1".repeat(32);
    static final String SESSION = "2".repeat(32);
    static final String I1 = "3".repeat(32);
    static final String I2 = "4".repeat(32);

    private Scripted() {
    }

    static byte[] frame(String type, int seq, Map<String, Object> body) {
        return frame(type, RUN, SESSION, seq, body);
    }

    static byte[] frame(String type, String run, String session, int seq, Map<String, Object> body) {
        ObjectNode node = Vectors.JSON.valueToTree(body);
        return Wire.encode(type, run, session, seq, node);
    }

    static byte[] start(int capacity) {
        return start(1, capacity);
    }

    static byte[] start(int seq, int capacity) {
        return frame("start", seq, Map.of("variant", "fixed", "params", Map.of("request_id", "1"),
                "commands", List.of("assign"), "points", List.of("after_read", "before_write"), "capacity", capacity,
                "database", Map.of("driver", "mysql", "host", "127.0.0.1", "port", 33060, "name", "weavegate",
                        "username", "synthetic", "password", "synthetic-only"),
                "startup_ms", 10000, "cancel_ms", 1000));
    }

    static byte[] invoke(int seq, String invocation, String worker, String command) {
        return frame("invoke", seq, Map.of("invocation", invocation, "worker", worker, "command", command));
    }

    static byte[] release(int seq, String invocation, String worker, String arrival, String point) {
        return frame("release", seq, Map.of("invocation", invocation, "worker", worker, "arrival", arrival, "point", point));
    }

    static byte[] cancel(int seq, String invocation, String worker, String reason) {
        return frame("cancel", seq, Map.of("invocation", invocation, "worker", worker, "reason", reason));
    }

    static byte[] stop(int seq, int budget) {
        return frame("stop", seq, Map.of("budget_ms", budget));
    }

    /** A started, ready peer with its probe lease returned. */
    static VectorHarness ready(int capacity) {
        VectorHarness h = new VectorHarness("independent").quiet();
        h.peer.receive(start(capacity));
        h.activity.awaitIdle();
        h.host.completeProbe();
        h.activity.awaitIdle();
        VectorHarness.require(last(h).get("type").stringValue().equals("ready"), "peer not ready");
        return h;
    }

    static JsonNode last(VectorHarness h) {
        return h.output.frames.getLast();
    }

    static void complete(VectorHarness h, String invocation, Peer.Transaction outcome) {
        Peer.Invocation inv;
        synchronized (h.peer) {
            inv = h.peer.invocations.get(invocation);
        }
        h.threads.run(invocation);
        h.activity.awaitIdle();
        h.peer.transactionBegun(inv);
        h.peer.leaseAcquired(inv);
        h.peer.transactionCompleted(inv, outcome);
        h.peer.leaseReturned(inv);
        h.host.script(invocation).mailbox.put(new Fakes.ExitProxy());
        h.activity.awaitIdle();
    }
}
