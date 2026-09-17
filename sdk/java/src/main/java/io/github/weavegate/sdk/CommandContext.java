package io.github.weavegate.sdk;

import java.util.Map;

/**
 * Immutable invocation data passed to a registered command. Identifiers are
 * opaque correlation values; they must not influence application invariants.
 *
 * @param invocation wire invocation identity
 * @param worker     scenario worker name
 * @param command    registered command name
 * @param variant    start variant, such as {@code vulnerable} or {@code fixed}
 * @param params     start parameters shared by every invocation in the session
 */
public record CommandContext(String invocation, String worker, String command, String variant,
                             Map<String, String> params) {
    public CommandContext {
        params = Map.copyOf(params);
    }
}
