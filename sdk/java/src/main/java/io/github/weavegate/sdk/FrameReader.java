package io.github.weavegate.sdk;

import java.io.IOException;
import java.io.InputStream;

/**
 * Reads length-prefixed frames. Partial pipe reads are assembled; the declared
 * length is validated before its payload buffer is allocated.
 */
final class FrameReader {
    private final InputStream in;
    private long allocatedPayloadBytes;

    FrameReader(InputStream in) {
        this.in = in;
    }

    /** Returns the next payload, or null for EOF exactly on a frame boundary. */
    byte[] next() throws IOException, Wire.WireException {
        byte[] header = new byte[4];
        int read = fill(header);
        if (read == 0) {
            return null;
        }
        if (read < header.length) {
            throw new Wire.WireException("transport", "EOF inside frame header");
        }
        long length = ((header[0] & 0xffL) << 24) | ((header[1] & 0xffL) << 16) | ((header[2] & 0xffL) << 8) | (header[3] & 0xffL);
        if (length < 1 || length > Wire.MAX_FRAME) {
            throw new Wire.WireException("protocol", "invalid frame length");
        }
        byte[] payload = new byte[(int) length];
        allocatedPayloadBytes += length;
        if (fill(payload) < payload.length) {
            throw new Wire.WireException("transport", "EOF inside frame payload");
        }
        return payload;
    }

    long allocatedPayloadBytes() {
        return allocatedPayloadBytes;
    }

    private int fill(byte[] buffer) throws IOException {
        int offset = 0;
        while (offset < buffer.length) {
            int n = in.read(buffer, offset, buffer.length - offset);
            if (n < 0) {
                return offset;
            }
            offset += n;
        }
        return offset;
    }
}
