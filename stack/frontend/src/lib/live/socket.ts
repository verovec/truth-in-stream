// Browser WebSocket implementation of the live transport port. The hook injects
// a fake in tests; this wraps the real WebSocket, sending binary PCM frames and
// surfacing text result frames. It guards send against a growing send buffer:
// for a live stream, dropping a frame under backpressure is recoverable while an
// unbounded buffer is not.
import type { LiveSocket, LiveSocketHandlers } from "./ports";

// maxBufferedBytes is roughly two seconds of 16 kHz 16-bit PCM. Past it, frames
// are dropped rather than queued.
const maxBufferedBytes = 64 * 1024;

export function createLiveSocket(
  url: string,
  handlers: LiveSocketHandlers,
): LiveSocket {
  const ws = new WebSocket(url);
  ws.binaryType = "arraybuffer";

  ws.addEventListener("open", () => handlers.onOpen());
  ws.addEventListener("message", (event: MessageEvent) => {
    // Result and subtitle frames are text; ignore any binary the server sends.
    if (typeof event.data === "string") {
      handlers.onFrame(event.data);
    }
  });
  ws.addEventListener("close", (event: CloseEvent) => {
    // A normal server closure (end of stream) is clean; an abrupt drop is not
    // and drives a reconnect.
    handlers.onClose(event.wasClean);
  });

  return {
    send: (frame) => {
      if (ws.readyState === WebSocket.OPEN && ws.bufferedAmount < maxBufferedBytes) {
        ws.send(frame);
      }
    },
    close: () => {
      // close() during CONNECTING throws in some engines; guard it.
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
        ws.close();
      }
    },
  };
}
