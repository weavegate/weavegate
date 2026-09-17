package io.github.weavegate.sdk.fixture;

import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;

/** Ordered milestones observed outside the SDK: body end, driver commit/rollback and physical close. */
public final class Journal {
    public static final List<String> EVENTS = new CopyOnWriteArrayList<>();

    private Journal() {
    }

    public static void add(String event) {
        EVENTS.add(event);
    }
}
