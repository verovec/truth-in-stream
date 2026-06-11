// The live capture/stream state machine. It is a pure reducer over playback and
// socket events that emits the side-effect commands the hook interprets
// (opening sockets, starting/suspending audio capture, scheduling reconnects).
// Keeping the control flow here, free of browser APIs, makes the hard part of
// the feature - pause/resume, seek reset, and reconnect/backoff - fully
// unit-testable.

// LiveSessionEvent is an input: a playback transition, a socket lifecycle
// signal, the reconnect timer firing, or teardown.
export type LiveSessionEvent =
  | { type: "play" }
  | { type: "pause" }
  | { type: "seek" }
  | { type: "socketOpen" }
  | { type: "socketClose"; clean: boolean }
  | { type: "reconnect" }
  | { type: "stop" };

// LiveSessionCommand is a side effect for the hook to perform. The hook owns the
// WebSocket, the AudioContext, and the reconnect timer; the reducer only names
// what to do next.
export type LiveSessionCommand =
  | "openSocket"
  | "closeSocket"
  | "startCapture"
  | "suspendCapture"
  | "resumeCapture"
  | "stopCapture"
  | "clearAnalysing"
  | "scheduleReconnect"
  | "cancelReconnect";

export type LivePhase =
  | "idle"
  | "connecting"
  | "streaming"
  | "paused"
  | "reconnecting"
  | "ended"
  | "failed";

// LiveSessionState is the machine's state. wantsPlay records the operator's
// intent (playing vs paused) independently of the connection phase, so a socket
// that opens after the operator has paused parks instead of streaming. attempts
// counts consecutive failed reconnects toward the cap.
export type LiveSessionState = {
  phase: LivePhase;
  attempts: number;
  wantsPlay: boolean;
};

// LiveStatus is the operator-facing rollup of the phase, surfaced in the panel.
export type LiveStatus =
  | "idle"
  | "connecting"
  | "live"
  | "paused"
  | "reconnecting"
  | "ended"
  | "error";

// MAX_RECONNECT_ATTEMPTS bounds reconnects before the session gives up and
// surfaces an error, so a backend that is down does not retry forever.
export const MAX_RECONNECT_ATTEMPTS = 5;

export function initialSession(): LiveSessionState {
  return { phase: "idle", attempts: 0, wantsPlay: false };
}

export function liveStatus(state: LiveSessionState): LiveStatus {
  switch (state.phase) {
    case "streaming":
      return "live";
    case "failed":
      return "error";
    default:
      return state.phase;
  }
}

type Reduction = { state: LiveSessionState; commands: LiveSessionCommand[] };

function emit(
  phase: LivePhase,
  attempts: number,
  wantsPlay: boolean,
  commands: LiveSessionCommand[],
): Reduction {
  return { state: { phase, attempts, wantsPlay }, commands };
}

// onUncleanClose decides the reconnect path shared by streaming and a reconnect
// attempt that dropped before opening: count the failure, then reconnect under
// the cap or give up.
function onUncleanClose(attempts: number, wantsPlay: boolean): Reduction {
  const next = attempts + 1;
  if (next > MAX_RECONNECT_ATTEMPTS) {
    return emit("failed", next, wantsPlay, ["stopCapture"]);
  }
  return emit("reconnecting", next, wantsPlay, [
    "stopCapture",
    "clearAnalysing",
    "scheduleReconnect",
  ]);
}

// A seek tears down the current session and starts a fresh one from the new
// position, preserving resolved verdicts (clearAnalysing keeps them) so the
// timeline never duplicates or loses a statement.
function onSeek(state: LiveSessionState): Reduction {
  if (state.phase === "idle") {
    return emit("idle", 0, state.wantsPlay, []);
  }
  const teardown: LiveSessionCommand[] =
    state.phase === "reconnecting"
      ? ["cancelReconnect", "clearAnalysing"]
      : ["closeSocket", "stopCapture", "clearAnalysing"];
  if (state.wantsPlay) {
    return emit("connecting", 0, true, [...teardown, "openSocket"]);
  }
  // Seeking while paused: stay down and reopen from the new position on play.
  return emit("idle", 0, false, teardown);
}

/**
 * Advances the session by one event, returning the next state and the commands
 * to perform. Pure and total: every phase handles every event.
 */
export function reduceSession(
  state: LiveSessionState,
  event: LiveSessionEvent,
): Reduction {
  if (event.type === "stop") {
    return emit("idle", 0, false, [
      "closeSocket",
      "stopCapture",
      "cancelReconnect",
    ]);
  }
  if (event.type === "seek") {
    return onSeek(state);
  }

  switch (state.phase) {
    case "idle":
      if (event.type === "play") {
        return emit("connecting", 0, true, ["openSocket"]);
      }
      return emit(state.phase, state.attempts, state.wantsPlay, []);

    case "connecting":
      switch (event.type) {
        case "play":
          return emit("connecting", state.attempts, true, []);
        case "pause":
          return emit("connecting", state.attempts, false, []);
        case "socketOpen":
          return state.wantsPlay
            ? emit("streaming", 0, true, ["startCapture"])
            : emit("paused", 0, false, []);
        case "socketClose":
          return event.clean
            ? emit("ended", 0, state.wantsPlay, ["stopCapture"])
            : onUncleanClose(state.attempts, state.wantsPlay);
        default:
          return emit(state.phase, state.attempts, state.wantsPlay, []);
      }

    case "streaming":
      switch (event.type) {
        case "pause":
          return emit("paused", state.attempts, false, ["suspendCapture"]);
        case "socketClose":
          return event.clean
            ? emit("ended", 0, state.wantsPlay, ["stopCapture"])
            : onUncleanClose(state.attempts, state.wantsPlay);
        default:
          return emit(state.phase, state.attempts, state.wantsPlay, []);
      }

    case "paused":
      switch (event.type) {
        case "play":
          return emit("streaming", state.attempts, true, ["resumeCapture"]);
        case "socketClose":
          // Down while paused: drop the capture graph and reopen on the next
          // play; keep resolved verdicts.
          return emit("idle", 0, false, ["stopCapture", "clearAnalysing"]);
        default:
          return emit(state.phase, state.attempts, false, []);
      }

    case "reconnecting":
      switch (event.type) {
        case "reconnect":
          return emit("connecting", state.attempts, state.wantsPlay, [
            "openSocket",
          ]);
        case "pause":
          return emit("idle", 0, false, ["cancelReconnect"]);
        default:
          return emit(state.phase, state.attempts, state.wantsPlay, []);
      }

    case "ended":
    case "failed":
      switch (event.type) {
        case "play":
          return emit("connecting", 0, true, ["openSocket"]);
        case "pause":
          return emit(state.phase, state.attempts, false, []);
        default:
          return emit(state.phase, state.attempts, state.wantsPlay, []);
      }
  }
}
