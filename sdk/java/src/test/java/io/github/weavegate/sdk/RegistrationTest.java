package io.github.weavegate.sdk;

import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.util.List;
import java.util.stream.Stream;
import javax.sql.DataSource;

import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;
import org.springframework.aop.framework.ProxyFactory;
import org.springframework.context.annotation.AnnotationConfigApplicationContext;
import org.springframework.context.annotation.Configuration;
import org.springframework.jdbc.datasource.DriverManagerDataSource;
import org.springframework.scheduling.annotation.AsyncAnnotationBeanPostProcessor;
import org.springframework.scheduling.annotation.ScheduledAnnotationBeanPostProcessor;
import org.springframework.test.util.ReflectionTestUtils;
import org.springframework.transaction.TransactionManager;
import org.springframework.transaction.annotation.AnnotationTransactionAttributeSource;
import org.springframework.transaction.annotation.EnableTransactionManagement;
import org.springframework.transaction.annotation.Transactional;
import org.springframework.transaction.interceptor.TransactionInterceptor;

class RegistrationTest {
    @Configuration(proxyBeanMethods = false)
    @EnableTransactionManagement(proxyTargetClass = true)
    static class Transactions { }

    public static class FinalCommands {
        @Transactional @WeavegateCommand("bad")
        public final void bad() { throw new AssertionError("must not dispatch"); }
        @Transactional @WeavegateCommand("good")
        public void good() { }
    }

    public static class StaticCommands {
        @Transactional @WeavegateCommand("bad")
        public static void bad() { throw new AssertionError("must not dispatch"); }
        @Transactional @WeavegateCommand("good")
        public void good() { }
    }

    public static class Commands {
        @Transactional @WeavegateCommand(value = "selected", points = "selected_point")
        public void selected() { }
        @Transactional @WeavegateCommand(value = "other", points = "other_point")
        public void other() { }
    }

    public static class TimeoutCommands {
        @Transactional(timeout = 1) @WeavegateCommand("timed")
        public void timed() { }
    }

    static void validate(Class<?> commands, List<String> selected, List<String> points) {
        validate(commands, selected, points, null);
    }

    static void validate(Class<?> commands, List<String> selected, List<String> points, Class<?> processor) {
        VectorHarness harness = new VectorHarness("independent").quiet();
        TrackingDataSource dataSource = new TrackingDataSource(new DriverManagerDataSource(), harness.peer);
        SpringHost host = new SpringHost(Transactions.class, new String[0]);
        host.bind(harness.peer);
        try (AnnotationConfigApplicationContext context = new AnnotationConfigApplicationContext()) {
            context.registerBean("dataSource", DataSource.class, () -> dataSource);
            context.registerBean("transactionManager", WeavegateTransactionManager.class,
                    () -> new WeavegateTransactionManager(dataSource, harness.peer));
            context.register(Transactions.class, commands);
            if (processor != null) {
                context.registerBean("customProcessor", processor);
            }
            context.refresh();
            ReflectionTestUtils.setField(host, "context", context);
            ReflectionTestUtils.setField(host, "dataSource", dataSource);
            host.validateRegistration(selected, points);
        }
    }

    @TestFactory
    Stream<DynamicTest> unadvisableCommandsFailRegistration() {
        return RequirementsTest.repeated(() -> {
            for (Class<?> type : List.of(FinalCommands.class, StaticCommands.class)) {
                assertThatThrownBy(() -> validate(type, List.of("bad"), List.of()))
                        .isInstanceOf(IllegalStateException.class);
            }
        });
    }

    @TestFactory
    Stream<DynamicTest> pointsBelongToSelectedCommands() {
        return RequirementsTest.repeated(() -> {
            validate(Commands.class, List.of("selected"), List.of("selected_point"));
            assertThatThrownBy(() -> validate(Commands.class, List.of("selected"), List.of("other_point")))
                    .isInstanceOf(IllegalStateException.class);
        });
    }

    @TestFactory
    Stream<DynamicTest> schedulingProcessorsAreRejectedByType() {
        return RequirementsTest.repeated(() -> {
            for (Class<?> processor : List.of(ScheduledAnnotationBeanPostProcessor.class,
                    AsyncAnnotationBeanPostProcessor.class)) {
                assertThatThrownBy(() -> validate(Commands.class, List.of("selected"), List.of(), processor))
                        .isInstanceOf(IllegalStateException.class);
            }
        });
    }

    @TestFactory
    Stream<DynamicTest> wallClockTransactionTimeoutsAreRejected() {
        return RequirementsTest.repeated(() ->
                assertThatThrownBy(() -> validate(TimeoutCommands.class, List.of("timed"), List.of()))
                        .isInstanceOf(IllegalStateException.class));
    }

    @TestFactory
    Stream<DynamicTest> commandAdviceMustUseRegisteredTransactionManager() {
        return RequirementsTest.repeated(() -> {
            VectorHarness harness = new VectorHarness("independent").quiet();
            TrackingDataSource dataSource = new TrackingDataSource(new DriverManagerDataSource(), harness.peer);
            WeavegateTransactionManager registered = new WeavegateTransactionManager(dataSource, harness.peer);
            WeavegateTransactionManager separate = new WeavegateTransactionManager(dataSource, harness.peer);
            ProxyFactory factory = new ProxyFactory(new Commands());
            factory.setProxyTargetClass(true);
            factory.addAdvice(new TransactionInterceptor(
                    (TransactionManager) separate, new AnnotationTransactionAttributeSource()));
            Commands proxy = (Commands) factory.getProxy();
            SpringHost host = new SpringHost(Transactions.class, new String[0]);
            host.bind(harness.peer);
            try (AnnotationConfigApplicationContext context = new AnnotationConfigApplicationContext()) {
                context.registerBean("dataSource", DataSource.class, () -> dataSource);
                context.registerBean("transactionManager", WeavegateTransactionManager.class, () -> registered);
                context.registerBean("commands", Commands.class, () -> proxy);
                context.refresh();
                ReflectionTestUtils.setField(host, "context", context);
                ReflectionTestUtils.setField(host, "dataSource", dataSource);
                assertThatThrownBy(() -> host.validateRegistration(List.of("selected"), List.of("selected_point")))
                        .isInstanceOf(IllegalStateException.class);
            }
        });
    }
}
