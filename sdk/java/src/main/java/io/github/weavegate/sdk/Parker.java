package io.github.weavegate.sdk;

/**
 * One-shot wait point. It reports parking to {@link Seams.Activity} so a
 * harness can observe that every signalled thread has made progress.
 */
class Parker {
    private final Seams.Activity activity;
    private boolean signalled;
    private boolean waiting;

    Parker(Seams.Activity activity) {
        this.activity = activity;
    }

    final void await() throws InterruptedException {
        synchronized (this) {
            while (!signalled) {
                if (!waiting) {
                    waiting = true;
                    activity.idle();
                }
                try {
                    wait();
                } catch (InterruptedException e) {
                    if (waiting && !signalled) {
                        waiting = false;
                        activity.busy();
                    }
                    throw e;
                }
            }
            waiting = false;
        }
    }

    /** Returns false if already signalled. */
    final boolean signal() {
        synchronized (this) {
            if (signalled) {
                return false;
            }
            signalled = true;
            if (waiting) {
                activity.busy();
            }
            notifyAll();
            return true;
        }
    }

    final synchronized boolean signalled() {
        return signalled;
    }

    final synchronized boolean parked() {
        return waiting;
    }
}
