package io.github.weavegate.sdk;

import java.util.List;
import java.util.stream.IntStream;
import java.util.stream.Stream;

import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.DynamicContainer;
import org.junit.jupiter.api.DynamicNode;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;
import tools.jackson.databind.JsonNode;

/** Runs every shared lifecycle case targeting Java, repeated as individual executions. */
class SharedVectorsTest {
    private static final Vectors VECTORS = Vectors.load();

    @TestFactory
    Stream<DynamicNode> lifecycleCases() {
        List<JsonNode> cases = VECTORS.javaCases();
        return cases.stream().map(c -> DynamicContainer.dynamicContainer(c.get("id").stringValue(),
                IntStream.rangeClosed(1, Vectors.repetitions()).mapToObj(n -> DynamicTest.dynamicTest("repetition " + n,
                        () -> new VectorHarness("case/" + c.get("id").stringValue()).run(VECTORS.steps(c))))));
    }

    @TestFactory
    Stream<DynamicNode> framingCases() {
        return VECTORS.javaFraming().stream().map(c -> DynamicContainer.dynamicContainer(c.get("id").stringValue(),
                IntStream.rangeClosed(1, Vectors.repetitions()).mapToObj(n -> DynamicTest.dynamicTest("repetition " + n,
                        () -> FramingHarness.run(c)))));
    }

    @AfterAll
    static void markers() {
        EvidenceListener.marker("EXTERNAL_SUT_JAVA_VECTOR_RESULT pin=" + VECTORS.revision + " cases="
                + VECTORS.javaCases().size() + " framing=" + VECTORS.javaFraming().size()
                + " dispatch=closed assertions=observed");
    }
}
