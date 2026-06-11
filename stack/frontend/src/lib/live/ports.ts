// Ports the live hook drives. The real implementations wrap the browser
// WebSocket and the Web Audio capture graph; tests inject fakes so the hook's
// orchestration is exercised without a network or an AudioContext.

// LiveSocket is the transport the hook sends PCM frames over. close() is the
// hook's intentional teardown; the peer closing is reported via onClose.
export type LiveSocket = {
  send: (frame: ArrayBuffer) => void;
  close: () => void;
};

export type LiveSocketHandlers = {
  onOpen: () => void;
  onFrame: (raw: string) => void;
  onClose: (clean: boolean) => void;
};

export type LiveSocketFactory = (
  url: string,
  handlers: LiveSocketHandlers,
) => LiveSocket;

// AudioCapture captures playback-rate PCM frames from a media element. resume()
// starts or continues capture and suspend() halts it. stop() ends a session's
// capture; the browser implementation halts the graph rather than destroying it,
// because a MediaElementAudioSourceNode can be built only once per element and
// the graph is reused across sessions. A later resume() restarts it.
export type AudioCapture = {
  resume: () => void;
  suspend: () => void;
  stop: () => void;
};

export type AudioCaptureFactory = (
  element: HTMLMediaElement,
  onFrame: (frame: ArrayBuffer) => void,
) => AudioCapture;
