package io.github.weavegate.sdk;

import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.List;
import java.util.Set;

import tools.jackson.databind.JsonNode;

/**
 * Feeds exact framing bytes to fresh decoders and a fresh peer. Valid frames are
 * also read coalesced and at every single-byte boundary.
 */
final class FramingHarness {
    static final String HANDLER = "sdk/java/src/test/java/io/github/weavegate/sdk/FramingHarness.java:FramingHarness.";

    private FramingHarness() {
    }

    /** Returns chunks in order, then EOF, or fails the test if bytes beyond the input are requested. */
    static final class ChunkedInput extends InputStream {
        private final byte[] data;
        private final java.util.ArrayDeque<Integer> chunks;
        private final boolean eof;
        private int offset;
        int reads;

        ChunkedInput(byte[] data, List<Integer> chunks, boolean eof) {
            this.data = data;
            this.chunks = new java.util.ArrayDeque<>(chunks);
            this.eof = eof;
        }

        @Override
        public int read() {
            byte[] one = new byte[1];
            return read(one, 0, 1) < 0 ? -1 : one[0] & 0xff;
        }

        @Override
        public int read(byte[] buffer, int off, int len) {
            if (offset == data.length) {
                if (eof) {
                    return -1;
                }
                throw new AssertionError("decoder requested bytes beyond the complete input");
            }
            int available = data.length - offset;
            int size = chunks.isEmpty() ? available : Math.min(chunks.peek(), available);
            int n = Math.min(size, len);
            if (!chunks.isEmpty()) {
                int head = chunks.pop();
                if (head > n) {
                    chunks.push(head - n);
                }
            }
            System.arraycopy(data, offset, buffer, off, n);
            offset += n;
            reads++;
            return n;
        }

        boolean exhausted() {
            return offset == data.length;
        }
    }

    static void run(JsonNode c) throws Exception {
        String row = "framing/" + c.get("id").stringValue();
        Set<String> fields = Set.of("id", "input_hex", "read_chunk_sizes", "eof", "expect", "decoded", "control_hex", "targets");
        c.propertyNames().forEach(name -> VectorHarness.require(fields.contains(name), "unknown framing field"));
        byte[] input = HexFormat.of().parseHex(c.get("input_hex").stringValue());
        List<Integer> chunks = new ArrayList<>();
        if (c.has("read_chunk_sizes")) {
            c.get("read_chunk_sizes").forEach(n -> chunks.add(n.intValue()));
        }
        boolean eof = c.has("eof") && c.get("eof").booleanValue();
        report(row, "input/input_hex", "run");
        if (c.has("read_chunk_sizes")) {
            report(row, "input/read_chunk_sizes", "run");
        }
        if (c.has("eof")) {
            report(row, "input/eof", "run");
        }

        if (c.has("control_hex")) {
            byte[] control = HexFormat.of().parseHex(c.get("control_hex").stringValue());
            FrameReader reader = new FrameReader(new ChunkedInput(control, new ArrayList<>(), false));
            Wire.decode(reader.next());
            report(row, "control/fresh_decoder_accepts", "run");
        }

        if (c.has("decoded")) {
            ChunkedInput stream = new ChunkedInput(input, new ArrayList<>(chunks), false);
            FrameReader reader = new FrameReader(stream);
            byte[] payload = reader.next();
            VectorHarness.require(stream.exhausted() && stream.reads == chunks.size() + 1,
                    "frame dispatched before or after the complete payload");
            Wire.Frame frame = Wire.decode(payload);
            JsonNode decoded = Vectors.JSON.readTree(payload);
            VectorHarness.require(decoded.equals(c.get("decoded")) && frame.type().equals(c.get("decoded").get("type").stringValue()),
                    "decoded frame mismatch");
            report(row, "observe/decoded", "run");
            // Also coalesced and at every single-byte boundary.
            VectorHarness.require(Vectors.JSON.readTree(new FrameReader(new ByteArrayInputStream(input)).next()).equals(decoded),
                    "coalesced read mismatch");
            List<Integer> single = new ArrayList<>();
            for (int i = 0; i < input.length; i++) {
                single.add(1);
            }
            ChunkedInput bytes = new ChunkedInput(input, single, false);
            VectorHarness.require(Vectors.JSON.readTree(new FrameReader(bytes).next()).equals(decoded) && bytes.reads == input.length,
                    "single-byte read mismatch");
            for (int i = 0; i < c.get("expect").size(); i++) {
                String label = c.get("expect").get(i).stringValue();
                VectorHarness.require(label.equals("one_ready_frame_after_complete_payload") && frame.type().equals("ready"),
                        "unhandled framing assertion " + label);
                report(row, "expect/" + i + "/" + label, "run");
            }
            return;
        }

        VectorHarness harness = new VectorHarness(row).quiet();
        ChunkedInput stream = new ChunkedInput(input, new ArrayList<>(chunks), eof);
        FrameReader reader = new FrameReader(stream);
        Transport.read(harness.peer, reader);
        harness.activity.awaitIdle();
        for (int i = 0; i < c.get("expect").size(); i++) {
            String label = c.get("expect").get(i).stringValue();
            switch (label) {
                case "fatal_protocol", "fatal_transport" -> {
                    String kind = label.substring("fatal_".length());
                    synchronized (harness.peer) {
                        VectorHarness.require(kind.equals(harness.peer.fatalKind) && harness.peer.start == null
                                && harness.exit.status() != null && harness.exit.status() == 1, "fatal not latched");
                    }
                    JsonNode fatal = harness.output.frames.getLast();
                    VectorHarness.require(fatal.get("type").stringValue().equals("fatal")
                            && fatal.get("body").get("kind").stringValue().equals(kind), "fatal frame not emitted");
                    if (kind.equals("protocol") && reader.allocatedPayloadBytes() > 0) {
                        // The rejection must come from the payload itself, not from a later direction check.
                        try {
                            Wire.decode(new FrameReader(new ByteArrayInputStream(input)).next());
                            throw new AssertionError("payload unexpectedly decoded");
                        } catch (Wire.WireException e) {
                            VectorHarness.require(e.kind().equals("protocol"), "unexpected rejection kind");
                        }
                    }
                }
                case "no_payload_allocation" -> VectorHarness.require(reader.allocatedPayloadBytes() == 0,
                        "payload allocated before length validation");
                case "no_dispatch" -> VectorHarness.require(harness.host.initializeCalls == 0
                        && harness.threads.submitted.isEmpty() && harness.peer.start == null, "frame dispatched");
                default -> throw new AssertionError("unhandled framing assertion " + label);
            }
            report(row, "expect/" + i + "/" + label, "run");
        }
    }

    private static void report(String row, String check, String method) {
        EvidenceListener.check(row, check, HANDLER + method);
    }

    static InputStream empty() throws IOException {
        return InputStream.nullInputStream();
    }
}
