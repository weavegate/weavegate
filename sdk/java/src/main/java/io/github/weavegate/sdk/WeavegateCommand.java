package io.github.weavegate.sdk;

import java.lang.annotation.Documented;
import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;

/**
 * Registers a public method of a transactional Spring bean as a named worker
 * command. The synchronous method returns {@code void} and takes no arguments
 * or one {@link CommandContext}. Its
 * {@code @Transactional} boundary must use {@code REQUIRED} propagation and
 * roll back for {@link WeavegateCancelledException}; the dispatcher calls it
 * through the bean proxy, never through self-invocation.
 */
@Documented
@Retention(RetentionPolicy.RUNTIME)
@Target(ElementType.METHOD)
public @interface WeavegateCommand {
    /** Command name sent by the engine. */
    String value();

    /** Sync-point names this command may reach. */
    String[] points() default {};
}
