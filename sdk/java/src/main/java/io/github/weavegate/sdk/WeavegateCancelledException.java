package io.github.weavegate.sdk;

/**
 * Thrown from a sync point after its invocation is canceled or the session
 * fails. Command transaction rules must roll back for this exception; it is
 * not a release and never resumes the canceled gate.
 */
public final class WeavegateCancelledException extends RuntimeException {
    private static final long serialVersionUID = 1L;

    WeavegateCancelledException(String message) {
        super(message, null, false, false);
    }
}
