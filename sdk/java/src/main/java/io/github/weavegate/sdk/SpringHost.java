package io.github.weavegate.sdk;

import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.lang.reflect.Modifier;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.Statement;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.function.UnaryOperator;
import java.util.regex.Pattern;

import javax.sql.DataSource;

import com.zaxxer.hikari.HikariDataSource;
import org.springframework.aop.support.AopUtils;
import org.springframework.boot.Banner;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.WebApplicationType;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.support.GenericApplicationContext;
import org.springframework.core.annotation.AnnotationUtils;
import org.springframework.core.env.MapPropertySource;
import org.springframework.scheduling.config.TaskManagementConfigUtils;
import org.springframework.transaction.TransactionDefinition;
import org.springframework.transaction.TransactionManager;
import org.springframework.transaction.annotation.AnnotationTransactionAttributeSource;
import org.springframework.transaction.interceptor.TransactionAttribute;
import org.springframework.util.ClassUtils;
import org.springframework.util.ReflectionUtils;

/**
 * Hosts one non-web Spring Boot application for one session. The SDK owns the
 * only DataSource and transaction manager; migrations, SQL initialization,
 * web servers, scheduling and async processing are rejected or disabled.
 */
final class SpringHost implements Seams.Host {
    private static final Pattern HOST = Pattern.compile("[A-Za-z0-9._-]+|\\[[0-9A-Fa-f:.]+]");
    private static final Map<String, Object> PROPERTIES = Map.of(
            "spring.main.web-application-type", "none",
            "spring.main.banner-mode", "off",
            "spring.sql.init.mode", "never",
            "spring.flyway.enabled", "false",
            "spring.liquibase.enabled", "false");

    private record Registered(Object bean, Method method, Set<String> points) {
    }

    private final Class<?> application;
    private final String[] args;
    private final UnaryOperator<DataSource> decorate;
    private Peer peer;
    private volatile HikariDataSource pool;
    private volatile ConfigurableApplicationContext context;
    private volatile TrackingDataSource dataSource;
    private final Map<String, Registered> registered = new HashMap<>();

    SpringHost(Class<?> application, String[] args) {
        this(application, args, UnaryOperator.identity());
    }

    /** The decorator is a test seam for injecting driver failures beneath lease tracking. */
    SpringHost(Class<?> application, String[] args, UnaryOperator<DataSource> decorate) {
        this.application = application;
        this.args = args.clone();
        this.decorate = decorate;
    }

    void bind(Peer owner) {
        this.peer = owner;
    }

    @Override
    public void initialize(Seams.Start start) {
        HikariDataSource hikari = new HikariDataSource();
        hikari.setPoolName("weavegate");
        hikari.setJdbcUrl(url(start.database()));
        hikari.setUsername(start.database().username());
        hikari.setPassword(start.database().password());
        // Probe plus one lease per worker; unrelated application work is rejected, not pooled.
        hikari.setMaximumPoolSize(start.capacity() + 1);
        hikari.setMinimumIdle(0);
        hikari.setInitializationFailTimeout(-1);
        pool = hikari;
        TrackingDataSource tracked = new TrackingDataSource(decorate.apply(hikari), peer);
        dataSource = tracked;
        WeavegateTransactionManager transactions = new WeavegateTransactionManager(tracked, peer);

        SpringApplication spring = new SpringApplication(application);
        spring.setWebApplicationType(WebApplicationType.NONE);
        spring.setBannerMode(Banner.Mode.OFF);
        spring.setRegisterShutdownHook(false);
        spring.addInitializers(ctx -> {
            ctx.getEnvironment().getPropertySources().addFirst(new MapPropertySource("weavegate", PROPERTIES));
            GenericApplicationContext generic = (GenericApplicationContext) ctx;
            generic.registerBean("dataSource", DataSource.class, () -> tracked, bd -> bd.setPrimary(true));
            generic.registerBean("transactionManager", WeavegateTransactionManager.class, () -> transactions,
                    bd -> bd.setPrimary(true));
        });
        context = spring.run(args);
    }

    @Override
    public void validateRegistration(List<String> commands, List<String> points) {
        ConfigurableApplicationContext ctx = context;
        Map<String, DataSource> dataSources = ctx.getBeansOfType(DataSource.class);
        if (dataSources.size() != 1 || dataSources.values().iterator().next() != dataSource) {
            throw new IllegalStateException("exactly one weavegate DataSource is supported");
        }
        Map<String, TransactionManager> managers = ctx.getBeansOfType(TransactionManager.class);
        if (managers.size() != 1 || !(managers.values().iterator().next() instanceof WeavegateTransactionManager)) {
            throw new IllegalStateException("exactly one weavegate transaction manager is supported");
        }
        if (ctx.containsBean(TaskManagementConfigUtils.SCHEDULED_ANNOTATION_PROCESSOR_BEAN_NAME)
                || ctx.containsBean(TaskManagementConfigUtils.ASYNC_ANNOTATION_PROCESSOR_BEAN_NAME)) {
            throw new IllegalStateException("scheduled and async processing are unsupported");
        }
        AnnotationTransactionAttributeSource attributes = new AnnotationTransactionAttributeSource();
        for (String name : ctx.getBeanDefinitionNames()) {
            Class<?> type = ctx.getType(name);
            if (type == null) {
                continue;
            }
            Class<?> user = ClassUtils.getUserClass(type);
            for (Method method : ReflectionUtils.getUniqueDeclaredMethods(user)) {
                WeavegateCommand command = AnnotationUtils.findAnnotation(method, WeavegateCommand.class);
                if (command == null) {
                    continue;
                }
                Object bean = ctx.getBean(name);
                if (registered.containsKey(command.value())) {
                    throw new IllegalStateException("duplicate command registration");
                }
                if (!Wire.name(command.value()) || !Modifier.isPublic(method.getModifiers())
                        || method.getParameterCount() > 1
                        || (method.getParameterCount() == 1 && method.getParameterTypes()[0] != CommandContext.class)) {
                    throw new IllegalStateException("invalid command signature");
                }
                if (!AopUtils.isAopProxy(bean)) {
                    throw new IllegalStateException("command bean is not proxied");
                }
                TransactionAttribute attribute = attributes.getTransactionAttribute(method, user);
                if (attribute == null || attribute.getPropagationBehavior() != TransactionDefinition.PROPAGATION_REQUIRED
                        || !attribute.rollbackOn(new WeavegateCancelledException("validation"))
                        || !(attribute.getQualifier() == null || attribute.getQualifier().isEmpty())) {
                    throw new IllegalStateException("command transaction must be REQUIRED and roll back on cancellation");
                }
                Set<String> declared = new HashSet<>(List.of(command.points()));
                if (!declared.stream().allMatch(Wire::name)) {
                    throw new IllegalStateException("invalid point registration");
                }
                registered.put(command.value(), new Registered(bean,
                        AopUtils.selectInvocableMethod(method, bean.getClass()), declared));
            }
        }
        Set<String> declaredPoints = new HashSet<>();
        registered.values().forEach(r -> declaredPoints.addAll(r.points()));
        if (!registered.keySet().containsAll(commands) || !declaredPoints.containsAll(points)) {
            throw new IllegalStateException("unsupported command or point");
        }
    }

    @Override
    public void probeDatabase() throws Exception {
        try (Connection connection = dataSource.getConnection(); Statement statement = connection.createStatement()) {
            statement.execute("SELECT 1");
        }
    }

    @Override
    public void cancelStartup() {
        // SpringApplication.run cannot be interrupted safely; the peer's startup/stop watchdogs bound it.
    }

    @Override
    public void execute(CommandContext command) throws Throwable {
        Registered target = registered.get(command.command());
        try {
            if (target.method().getParameterCount() == 0) {
                target.method().invoke(target.bean());
            } else {
                target.method().invoke(target.bean(), command);
            }
        } catch (InvocationTargetException e) {
            throw e.getCause();
        }
    }

    @Override
    public void close() {
        ConfigurableApplicationContext ctx = context;
        if (ctx != null) {
            ctx.close();
        }
        HikariDataSource hikari = pool;
        if (hikari != null) {
            hikari.close();
            if (!hikari.isClosed()) {
                throw new IllegalStateException("pool did not close");
            }
        }
    }

    static String url(Seams.Database database) {
        if (!HOST.matcher(database.host()).matches()) {
            throw new IllegalArgumentException("unsupported database host");
        }
        String name = URLEncoder.encode(database.name(), StandardCharsets.UTF_8).replace("+", "%20");
        return "jdbc:mysql://" + database.host() + ":" + database.port() + "/" + name;
    }
}
