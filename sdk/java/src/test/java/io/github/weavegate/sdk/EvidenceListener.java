package io.github.weavegate.sdk;

import java.io.IOException;
import java.io.PrintWriter;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.regex.Pattern;

import com.zaxxer.hikari.HikariDataSource;
import org.junit.platform.engine.TestExecutionResult;
import org.junit.platform.engine.UniqueId;
import org.junit.platform.launcher.TestExecutionListener;
import org.junit.platform.launcher.TestIdentifier;
import org.junit.platform.launcher.TestPlan;
import org.springframework.boot.SpringBootVersion;
import org.springframework.core.SpringVersion;
import tools.jackson.databind.json.JsonMapper;

/**
 * Writes the execution boundaries that the Java acceptance recorder trusts.
 * Checks can only be attributed to a test execution that this listener saw
 * start and finish; pass/fail comes from JUnit, not from the harness.
 */
public final class EvidenceListener implements TestExecutionListener {
    private static final Pattern REPETITION = Pattern.compile("#(\\d+)");
    private static final JsonMapper JSON = JsonMapper.shared();
    private static PrintWriter log;

    @Override
    public void testPlanExecutionStarted(TestPlan plan) {
        String target = System.getProperty("weavegate.evidence");
        if (target == null) {
            return;
        }
        try {
            Path path = Path.of(target);
            Files.createDirectories(path.getParent());
            log = new PrintWriter(Files.newBufferedWriter(path, StandardCharsets.UTF_8), true);
        } catch (IOException e) {
            throw new IllegalStateException("cannot open evidence log", e);
        }
        Map<String, String> versions = new LinkedHashMap<>();
        versions.put("java", System.getProperty("java.version") + " " + System.getProperty("java.vendor"));
        versions.put("spring", "Spring Boot " + SpringBootVersion.getVersion() + ", Spring Framework " + SpringVersion.getVersion());
        versions.put("transaction_manager", "DataSourceTransactionManager (spring-jdbc " + SpringVersion.getVersion() + ")");
        versions.put("jdbc_driver", "MySQL Connector/J " + com.mysql.cj.Constants.CJ_VERSION);
        versions.put("pool", "HikariCP " + mavenVersion(HikariDataSource.class, "com.zaxxer", "HikariCP"));
        versions.put("build_tool", "Apache Maven " + System.getProperty("weavegate.maven"));
        write("EXTERNAL_SUT_VERSIONS " + JSON.writeValueAsString(versions));
    }

    private static String mavenVersion(Class<?> type, String group, String artifact) {
        try (var in = type.getResourceAsStream("/META-INF/maven/" + group + "/" + artifact + "/pom.properties")) {
            java.util.Properties properties = new java.util.Properties();
            properties.load(in);
            return properties.getProperty("version");
        } catch (IOException | NullPointerException e) {
            throw new IllegalStateException("cannot resolve " + artifact + " version", e);
        }
    }

    @Override
    public void testPlanExecutionFinished(TestPlan plan) {
        synchronized (EvidenceListener.class) {
            if (log != null) {
                log.close();
                log = null;
            }
        }
    }

    @Override
    public void executionStarted(TestIdentifier id) {
        if (id.isTest()) {
            write("EXTERNAL_SUT_TEST_RUN " + JSON.writeValueAsString(identity(id)));
        }
    }

    @Override
    public void executionSkipped(TestIdentifier id, String reason) {
        if (id.isTest()) {
            write("EXTERNAL_SUT_TEST_SKIP " + JSON.writeValueAsString(identity(id)));
        }
    }

    @Override
    public void executionFinished(TestIdentifier id, TestExecutionResult result) {
        if (id.isTest()) {
            String status = result.getStatus() == TestExecutionResult.Status.SUCCESSFUL ? "PASS" : "FAIL";
            write("EXTERNAL_SUT_TEST_" + status + " " + JSON.writeValueAsString(identity(id)));
        }
    }

    /** Records one observed check inside the currently executing test. */
    static void check(String row, String check, String handler) {
        Map<String, String> entry = new LinkedHashMap<>();
        entry.put("row", row);
        entry.put("check", check);
        entry.put("handler", handler);
        write("EXTERNAL_SUT_CHECK " + JSON.writeValueAsString(entry));
    }

    /** Fixed-phrase CI marker, written to both the evidence log and stdout. */
    static void marker(String text) {
        System.out.println(text);
        write(text);
    }

    private static synchronized void write(String line) {
        if (log != null) {
            log.println(line);
        }
    }

    private static Map<String, Object> identity(TestIdentifier id) {
        List<UniqueId.Segment> segments = UniqueId.parse(id.getUniqueId()).getSegments();
        UniqueId.Segment last = segments.getLast();
        var matcher = REPETITION.matcher(last.getValue());
        boolean repeated = (last.getType().equals("dynamic-test") || last.getType().equals("test-template-invocation"))
                && matcher.matches();
        StringBuilder test = new StringBuilder();
        for (UniqueId.Segment segment : repeated ? segments.subList(0, segments.size() - 1) : segments) {
            test.append('[').append(segment.getType()).append(':').append(segment.getValue()).append(']');
        }
        Map<String, Object> identity = new LinkedHashMap<>();
        identity.put("test", test.toString());
        identity.put("repetition", repeated ? Integer.parseInt(matcher.group(1)) : 1);
        return identity;
    }
}
