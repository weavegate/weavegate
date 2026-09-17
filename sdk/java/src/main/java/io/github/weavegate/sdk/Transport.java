package io.github.weavegate.sdk;

import java.io.IOException;
import java.io.InputStream;

/** The single control reader. It never blocks on workers, gates or application work. */
final class Transport {
    private Transport() {
    }

    static void read(Peer peer, FrameReader reader) {
        while (true) {
            byte[] payload;
            try {
                payload = reader.next();
            } catch (Wire.WireException e) {
                peer.readFailed(e.kind(), e.getMessage());
                return;
            } catch (IOException e) {
                peer.readFailed("transport", "control input failed");
                return;
            }
            if (payload == null) {
                peer.readFailed("transport", "control input closed");
                return;
            }
            if (!peer.receive(payload)) {
                // After fatal, stopped or forced exit the peer need not read further input.
                return;
            }
        }
    }

    static Thread startReader(Peer peer, InputStream in) {
        Thread thread = new Thread(() -> read(peer, new FrameReader(in)), "weavegate-reader");
        thread.setDaemon(true);
        thread.start();
        return thread;
    }
}
