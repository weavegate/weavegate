package io.github.weavegate.sdk;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.List;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

/** Loads the shared vectors only after verifying the reviewed acceptance pin. */
final class Vectors {
    static final JsonMapper JSON = JsonMapper.shared();

    final JsonNode data;
    final String revision;

    private Vectors(JsonNode data, String revision) {
        this.data = data;
        this.revision = revision;
    }

    static Vectors load() {
        try {
            Path root = Path.of(System.getProperty("weavegate.repository", "../..")).toAbsolutePath().normalize();
            JsonNode plan = JSON.readTree(root.resolve("docs/reference/testdata/external-sut-acceptance.json").toFile());
            JsonNode vector = plan.get("vector");
            byte[] bytes = Files.readAllBytes(root.resolve(vector.get("path").stringValue()));
            String digest = HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(bytes));
            if (!digest.equals(vector.get("sha256").stringValue())) {
                throw new IllegalStateException("shared vectors differ from the reviewed pin");
            }
            return new Vectors(JSON.readTree(bytes), vector.get("revision").stringValue());
        } catch (IOException | NoSuchAlgorithmException e) {
            throw new IllegalStateException("cannot load shared vectors", e);
        }
    }

    static int repetitions() {
        int n = Integer.parseInt(System.getProperty("weavegate.repetitions", "1"));
        if (n < 1) {
            throw new IllegalStateException("repetitions must be positive");
        }
        return n;
    }

    List<JsonNode> javaCases() {
        List<JsonNode> cases = new ArrayList<>();
        for (JsonNode c : data.get("cases")) {
            if (targets(c)) {
                cases.add(c);
            }
        }
        return cases;
    }

    List<JsonNode> javaFraming() {
        List<JsonNode> cases = new ArrayList<>();
        for (JsonNode c : data.get("framing")) {
            if (targets(c)) {
                cases.add(c);
            }
        }
        return cases;
    }

    List<JsonNode> steps(JsonNode c) {
        List<JsonNode> steps = new ArrayList<>();
        expand(c.get("prefix").stringValue(), steps);
        c.get("steps").forEach(steps::add);
        return steps;
    }

    private void expand(String prefix, List<JsonNode> steps) {
        JsonNode history = data.get("prefixes").get(prefix);
        if (history == null) {
            throw new IllegalStateException("unknown vector prefix");
        }
        for (JsonNode step : history) {
            if (step.has("prefix")) {
                expand(step.get("prefix").stringValue(), steps);
            } else {
                steps.add(step);
            }
        }
    }

    private static boolean targets(JsonNode c) {
        for (JsonNode target : c.get("targets")) {
            if (target.stringValue().equals("java")) {
                return true;
            }
        }
        return false;
    }
}
