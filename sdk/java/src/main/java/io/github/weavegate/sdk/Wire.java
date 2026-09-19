package io.github.weavegate.sdk;

import java.math.BigInteger;
import java.nio.ByteBuffer;
import java.nio.charset.CharacterCodingException;
import java.nio.charset.CodingErrorAction;
import java.nio.charset.StandardCharsets;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.regex.Pattern;

import tools.jackson.core.JacksonException;
import tools.jackson.core.StreamReadFeature;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ArrayNode;
import tools.jackson.databind.node.JsonNodeFactory;
import tools.jackson.databind.node.ObjectNode;

/**
 * Strict wire v1 codec. Every decoded frame has passed UTF-8, duplicate-key,
 * trailing-value, envelope, version and closed body-schema validation; peers
 * only check direction and state.
 */
final class Wire {
    static final int MAX_FRAME = 1 << 20;
    static final int MAX_SEQ = 100_000;
    static final int MAX_ARRIVAL = 100_000;
    static final int MAX_MESSAGE_BYTES = 1024;

    private static final JsonMapper MAPPER = JsonMapper.builder()
            .enable(StreamReadFeature.STRICT_DUPLICATE_DETECTION)
            .enable(DeserializationFeature.FAIL_ON_TRAILING_TOKENS)
            .enable(DeserializationFeature.FAIL_ON_READING_DUP_TREE_KEY)
            .enable(DeserializationFeature.USE_BIG_INTEGER_FOR_INTS)
            .build();
    private static final Pattern HEX32 = Pattern.compile("[0-9a-f]{32}");
    private static final Pattern ARRIVAL = Pattern.compile("[1-9][0-9]{0,5}");
    private static final Pattern SQL_STATE = Pattern.compile("[0-9A-Z]{5}");
    private static final List<String> ENVELOPE = List.of("v", "type", "run", "session", "seq", "body");
    private static final Map<String, List<String>> BODIES = Map.ofEntries(
            Map.entry("start", List.of("variant", "params", "commands", "points", "capacity", "database", "startup_ms", "cancel_ms")),
            Map.entry("ready", List.of("commands", "points", "capacity")),
            Map.entry("invoke", List.of("invocation", "worker", "command")),
            Map.entry("accepted", List.of("invocation", "worker")),
            Map.entry("arrive", List.of("invocation", "worker", "arrival", "point")),
            Map.entry("release", List.of("invocation", "worker", "arrival", "point")),
            Map.entry("terminal", List.of("invocation", "worker", "transaction", "connection", "error")),
            Map.entry("cancel", List.of("invocation", "worker", "reason")),
            Map.entry("stop", List.of("budget_ms")),
            Map.entry("stopped", List.of()),
            Map.entry("fatal", List.of("kind", "message")));
    private static final Set<String> FATAL_KINDS = Set.of("version", "protocol", "startup", "transport", "transaction", "cleanup", "shutdown");

    private Wire() {
    }

    /** A validated frame; payload retains the exact received bytes for duplicate digests. */
    record Frame(String type, String run, String session, int seq, ObjectNode body, byte[] payload) {
        String text(String field) {
            return body.get(field).stringValue();
        }

        int integer(String field) {
            return body.get(field).intValue();
        }
    }

    /** Rejection before dispatch. Kind is a fatal wire kind. */
    static final class WireException extends Exception {
        private final String kind;
        private final String run;
        private final String session;

        WireException(String kind, String message) {
            this(kind, message, null, null);
        }

        WireException(String kind, String message, String run, String session) {
            super(message, null, false, false);
            this.kind = kind;
            this.run = run;
            this.session = session;
        }

        String kind() {
            return kind;
        }

        /** Envelope identity of a well-formed frame rejected for its version, if present. */
        String run() {
            return run;
        }

        String session() {
            return session;
        }
    }

    static ObjectNode object() {
        return JsonNodeFactory.instance.objectNode();
    }

    static Frame decode(byte[] payload) throws WireException {
        if (payload.length < 1 || payload.length > MAX_FRAME) {
            throw protocol("invalid frame length");
        }
        try {
            StandardCharsets.UTF_8.newDecoder()
                    .onMalformedInput(CodingErrorAction.REPORT)
                    .onUnmappableCharacter(CodingErrorAction.REPORT)
                    .decode(ByteBuffer.wrap(payload));
        } catch (CharacterCodingException e) {
            throw protocol("invalid UTF-8");
        }
        JsonNode root;
        try {
            root = MAPPER.readTree(payload);
        } catch (JacksonException e) {
            throw protocol("invalid JSON");
        }
        if (root == null || !root.isObject()) {
            throw protocol("frame is not an object");
        }
        exactFields(root, ENVELOPE);
        JsonNode v = root.get("v");
        if (!v.isIntegralNumber()) {
            throw protocol("invalid version field");
        }
        if (!v.bigIntegerValue().equals(BigInteger.ONE)) {
            JsonNode run = root.get("run");
            JsonNode session = root.get("session");
            boolean identified = run.isString() && HEX32.matcher(run.stringValue()).matches()
                    && session.isString() && HEX32.matcher(session.stringValue()).matches();
            throw new WireException("version", "unsupported wire version " + boundedDecimal(v.bigIntegerValue()),
                    identified ? run.stringValue() : null, identified ? session.stringValue() : null);
        }
        String type = string(root.get("type"));
        List<String> fields = BODIES.get(type);
        if (fields == null) {
            throw protocol("unknown message type");
        }
        String run = hex32(root.get("run"));
        String session = hex32(root.get("session"));
        int seq = integer(root.get("seq"), 1, MAX_SEQ);
        JsonNode body = root.get("body");
        if (!body.isObject()) {
            throw protocol("body is not an object");
        }
        exactFields(body, fields);
        validateBody(type, (ObjectNode) body);
        return new Frame(type, run, session, seq, (ObjectNode) body, payload);
    }

    static byte[] encode(String type, String run, String session, int seq, ObjectNode body) {
        ObjectNode root = object();
        root.put("v", 1);
        root.put("type", type);
        root.put("run", run);
        root.put("session", session);
        root.put("seq", seq);
        root.set("body", body);
        return MAPPER.writeValueAsBytes(root);
    }

    static void validateName(String value) throws WireException {
        if (!name(value)) {
            throw protocol("invalid name");
        }
    }

    static boolean name(String value) {
        if (value == null || value.isEmpty() || !scalar(value) || value.getBytes(StandardCharsets.UTF_8).length > 128) {
            return false;
        }
        if (whitespace(value.codePointAt(0)) || whitespace(value.codePointBefore(value.length()))) {
            return false;
        }
        return value.codePoints().noneMatch(c -> Character.getType(c) == Character.CONTROL);
    }

    /** Replaces control characters and truncates to the wire's UTF-8 byte bound on a code point boundary. */
    static String sanitize(String message) {
        if (message == null) {
            return "";
        }
        StringBuilder out = new StringBuilder();
        int bytes = 0;
        for (int i = 0; i < message.length(); ) {
            int c = message.codePointAt(i);
            i += Character.charCount(c);
            if (Character.getType(c) == Character.CONTROL || Character.getType(c) == Character.SURROGATE) {
                c = ' ';
            }
            int width = new String(Character.toChars(c)).getBytes(StandardCharsets.UTF_8).length;
            if (bytes + width > MAX_MESSAGE_BYTES) {
                break;
            }
            out.appendCodePoint(c);
            bytes += width;
        }
        return out.toString();
    }

    private static void validateBody(String type, ObjectNode body) throws WireException {
        switch (type) {
            case "start" -> {
                validateName(string(body.get("variant")));
                JsonNode params = body.get("params");
                if (!params.isObject()) {
                    throw protocol("invalid params");
                }
                for (Map.Entry<String, JsonNode> entry : params.properties()) {
                    validateName(entry.getKey());
                    string(entry.getValue());
                }
                names(body.get("commands"), true);
                names(body.get("points"), false);
                integer(body.get("capacity"), 1, 1024);
                database(body.get("database"));
                integer(body.get("startup_ms"), 1, Integer.MAX_VALUE);
                integer(body.get("cancel_ms"), 1, Integer.MAX_VALUE);
            }
            case "ready" -> {
                names(body.get("commands"), true);
                names(body.get("points"), false);
                integer(body.get("capacity"), 1, 1024);
            }
            case "invoke" -> {
                identity(body);
                validateName(string(body.get("command")));
            }
            case "accepted" -> identity(body);
            case "arrive", "release" -> {
                identity(body);
                arrival(body.get("arrival"));
                validateName(string(body.get("point")));
            }
            case "terminal" -> terminal(body);
            case "cancel" -> {
                identity(body);
                String reason = string(body.get("reason"));
                if (!reason.equals("context") && !reason.equals("stop")) {
                    throw protocol("invalid cancel reason");
                }
            }
            case "stop" -> integer(body.get("budget_ms"), 1, Integer.MAX_VALUE);
            case "stopped" -> {
            }
            case "fatal" -> {
                if (!FATAL_KINDS.contains(string(body.get("kind")))) {
                    throw protocol("invalid fatal kind");
                }
                message(body.get("message"));
            }
            default -> throw protocol("unknown message type");
        }
    }

    private static void terminal(ObjectNode body) throws WireException {
        identity(body);
        String transaction = string(body.get("transaction"));
        String connection = string(body.get("connection"));
        if (!Set.of("committed", "rolled_back", "not_started").contains(transaction)
                || !Set.of("returned", "not_acquired").contains(connection)
                || (connection.equals("not_acquired") && !transaction.equals("not_started"))) {
            throw protocol("invalid terminal outcome");
        }
        JsonNode error = body.get("error");
        if (error.isNull()) {
            if (!transaction.equals("committed")) {
                throw protocol("terminal error required");
            }
            return;
        }
        if (!error.isObject()) {
            throw protocol("invalid terminal error");
        }
        exactFields(error, List.of("kind", "message", "mysql_code", "sql_state"));
        String kind = string(error.get("kind"));
        message(error.get("message"));
        int code = integer(error.get("mysql_code"), 0, 65535);
        String state = string(error.get("sql_state"));
        boolean valid = switch (kind) {
            case "mysql" -> code != 0 && SQL_STATE.matcher(state).matches();
            case "application", "cancelled" -> code == 0 && state.isEmpty();
            default -> false;
        };
        if (!valid) {
            throw protocol("invalid terminal error");
        }
    }

    private static void database(JsonNode database) throws WireException {
        if (!database.isObject()) {
            throw protocol("invalid database");
        }
        exactFields(database, List.of("driver", "host", "port", "name", "username", "password"));
        if (!string(database.get("driver")).equals("mysql") || string(database.get("host")).isEmpty()
                || string(database.get("name")).isEmpty() || string(database.get("username")).isEmpty()) {
            throw protocol("invalid database");
        }
        string(database.get("password"));
        integer(database.get("port"), 1, 65535);
    }

    private static void identity(JsonNode body) throws WireException {
        hex32(body.get("invocation"));
        validateName(string(body.get("worker")));
    }

    private static void arrival(JsonNode node) throws WireException {
        String value = string(node);
        if (!ARRIVAL.matcher(value).matches() || Integer.parseInt(value) > MAX_ARRIVAL) {
            throw protocol("invalid arrival");
        }
    }

    private static void message(JsonNode node) throws WireException {
        if (string(node).getBytes(StandardCharsets.UTF_8).length > MAX_MESSAGE_BYTES) {
            throw protocol("message too long");
        }
    }

    private static void names(JsonNode node, boolean nonEmpty) throws WireException {
        if (!node.isArray() || (nonEmpty && node.isEmpty())) {
            throw protocol("invalid name array");
        }
        Set<String> seen = new HashSet<>();
        for (JsonNode item : (ArrayNode) node) {
            String value = string(item);
            validateName(value);
            if (!seen.add(value)) {
                throw protocol("duplicate name");
            }
        }
    }

    private static void exactFields(JsonNode node, List<String> fields) throws WireException {
        if (node.size() != fields.size()) {
            throw protocol("unexpected fields");
        }
        for (String field : fields) {
            if (!node.has(field)) {
                throw protocol("missing field");
            }
        }
    }

    private static String string(JsonNode node) throws WireException {
        if (node == null || !node.isString() || !scalar(node.stringValue())) {
            throw protocol("invalid string");
        }
        return node.stringValue();
    }

    private static String hex32(JsonNode node) throws WireException {
        String value = string(node);
        if (!HEX32.matcher(value).matches()) {
            throw protocol("invalid identity");
        }
        return value;
    }

    private static int integer(JsonNode node, long min, long max) throws WireException {
        if (node == null || !node.isIntegralNumber()) {
            throw protocol("invalid integer");
        }
        BigInteger value = node.bigIntegerValue();
        if (value.compareTo(BigInteger.valueOf(min)) < 0 || value.compareTo(BigInteger.valueOf(max)) > 0) {
            throw protocol("integer out of range");
        }
        return value.intValue();
    }

    private static boolean scalar(String value) {
        for (int i = 0; i < value.length(); i++) {
            char c = value.charAt(i);
            if (Character.isHighSurrogate(c)) {
                if (i + 1 >= value.length() || !Character.isLowSurrogate(value.charAt(i + 1))) {
                    return false;
                }
                i++;
            } else if (Character.isLowSurrogate(c)) {
                return false;
            }
        }
        return true;
    }

    private static boolean whitespace(int c) {
        return Character.isWhitespace(c) || Character.isSpaceChar(c);
    }

    private static String boundedDecimal(BigInteger value) {
        String text = value.toString();
        return text.length() <= 20 ? text : text.substring(0, 20);
    }

    private static WireException protocol(String message) {
        return new WireException("protocol", message);
    }
}
