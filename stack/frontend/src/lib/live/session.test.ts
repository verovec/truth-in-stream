import { describe, expect, test } from "vitest";
import {
  initialSession,
  type LiveSessionEvent,
  type LiveSessionState,
  liveStatus,
  MAX_RECONNECT_ATTEMPTS,
  reduceSession,
} from "./session";

// drive replays a sequence of events from a starting state, returning the final
// state and the commands emitted by the last event.
function drive(
  start: LiveSessionState,
  events: LiveSessionEvent[],
): { state: LiveSessionState; commands: string[] } {
  let state = start;
  let commands: string[] = [];
  for (const event of events) {
    const next = reduceSession(state, event);
    state = next.state;
    commands = next.commands;
  }
  return { state, commands };
}

describe("reduceSession", () => {
  test("playing from idle opens the socket", () => {
    const { state, commands } = drive(initialSession(), [{ type: "play" }]);
    expect(state.phase).toBe("connecting");
    expect(commands).toEqual(["openSocket"]);
  });

  test("the socket opening while playing starts capture and goes live", () => {
    const { state, commands } = drive(initialSession(), [
      { type: "play" },
      { type: "socketOpen" },
    ]);
    expect(state.phase).toBe("streaming");
    expect(liveStatus(state)).toBe("live");
    expect(commands).toEqual(["startCapture"]);
  });

  test("pausing before the socket opens parks it without capturing", () => {
    const { state, commands } = drive(initialSession(), [
      { type: "play" },
      { type: "pause" },
      { type: "socketOpen" },
    ]);
    expect(state.phase).toBe("paused");
    expect(commands).not.toContain("startCapture");
  });

  test("pausing suspends capture and resuming continues it without reopening", () => {
    const live = drive(initialSession(), [
      { type: "play" },
      { type: "socketOpen" },
    ]).state;

    const paused = reduceSession(live, { type: "pause" });
    expect(paused.state.phase).toBe("paused");
    expect(paused.commands).toEqual(["suspendCapture"]);

    const resumed = reduceSession(paused.state, { type: "play" });
    expect(resumed.state.phase).toBe("streaming");
    expect(resumed.commands).toEqual(["resumeCapture"]);
    // Resuming must not open a new socket.
    expect(resumed.commands).not.toContain("openSocket");
  });

  test("seeking while live resets the session: closes, clears in-flight, reopens", () => {
    const live = drive(initialSession(), [
      { type: "play" },
      { type: "socketOpen" },
    ]).state;

    const { state, commands } = reduceSession(live, { type: "seek" });
    expect(state.phase).toBe("connecting");
    expect(commands).toEqual([
      "closeSocket",
      "stopCapture",
      "clearAnalysing",
      "openSocket",
    ]);
  });

  test("an unclean drop while live schedules a reconnect", () => {
    const live = drive(initialSession(), [
      { type: "play" },
      { type: "socketOpen" },
    ]).state;

    const { state, commands } = reduceSession(live, {
      type: "socketClose",
      clean: false,
    });
    expect(state.phase).toBe("reconnecting");
    expect(state.attempts).toBe(1);
    expect(commands).toEqual([
      "stopCapture",
      "clearAnalysing",
      "scheduleReconnect",
    ]);
    expect(liveStatus(state)).toBe("reconnecting");
  });

  test("the reconnect timer reopens the socket and a clean reconnect resumes", () => {
    const state = drive(initialSession(), [
      { type: "play" },
      { type: "socketOpen" },
      { type: "socketClose", clean: false },
    ]).state;

    const reopened = reduceSession(state, { type: "reconnect" });
    expect(reopened.state.phase).toBe("connecting");
    expect(reopened.commands).toEqual(["openSocket"]);

    const back = reduceSession(reopened.state, { type: "socketOpen" });
    expect(back.state.phase).toBe("streaming");
    expect(back.commands).toEqual(["startCapture"]);
    // Attempt counter resets once back to streaming.
    expect(back.state.attempts).toBe(0);
  });

  test("a clean close ends the stream without reconnecting", () => {
    const live = drive(initialSession(), [
      { type: "play" },
      { type: "socketOpen" },
    ]).state;

    const { state, commands } = reduceSession(live, {
      type: "socketClose",
      clean: true,
    });
    expect(state.phase).toBe("ended");
    expect(commands).toEqual(["stopCapture"]);
    expect(commands).not.toContain("scheduleReconnect");
  });

  test("reconnects that never re-establish give up after the attempt cap", () => {
    // Each drop is followed by a reconnect that fails to open (another unclean
    // close), so attempts accumulate instead of resetting.
    let state = drive(initialSession(), [
      { type: "play" },
      { type: "socketOpen" },
    ]).state;
    let drops = 0;
    let lastCommands: string[] = [];
    while (state.phase !== "failed" && drops < 20) {
      const closed = reduceSession(state, { type: "socketClose", clean: false });
      state = closed.state;
      lastCommands = closed.commands;
      drops++;
      if (state.phase === "reconnecting") {
        state = reduceSession(state, { type: "reconnect" }).state;
      }
    }
    expect(state.phase).toBe("failed");
    expect(drops).toBe(MAX_RECONNECT_ATTEMPTS + 1);
    expect(liveStatus(state)).toBe("error");
    expect(lastCommands).not.toContain("scheduleReconnect");
    expect(lastCommands).toContain("stopCapture");
  });

  test("stop tears everything down from any phase", () => {
    const live = drive(initialSession(), [
      { type: "play" },
      { type: "socketOpen" },
    ]).state;

    const { state, commands } = reduceSession(live, { type: "stop" });
    expect(state.phase).toBe("idle");
    expect(commands).toEqual(["closeSocket", "stopCapture", "cancelReconnect"]);
  });

  test("playing again after a clean end reopens the socket", () => {
    const ended = drive(initialSession(), [
      { type: "play" },
      { type: "socketOpen" },
      { type: "socketClose", clean: true },
    ]).state;

    const { state, commands } = reduceSession(ended, { type: "play" });
    expect(state.phase).toBe("connecting");
    expect(commands).toEqual(["openSocket"]);
  });
});
