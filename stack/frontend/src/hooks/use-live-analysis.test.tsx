import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import {
  PlaybackProvider,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import type { PlaybackStore } from "@/lib/playback/playback-store";
import type {
  AudioCaptureFactory,
  LiveSocketHandlers,
} from "@/lib/live/ports";
import { type LiveAnalysis, useLiveAnalysis } from "./use-live-analysis";

type FakeSocket = {
  url: string;
  handlers: LiveSocketHandlers;
  send: ReturnType<typeof vi.fn<(frame: ArrayBuffer) => void>>;
  close: ReturnType<typeof vi.fn<() => void>>;
};

type FakeCapture = {
  onFrame: (frame: ArrayBuffer) => void;
  resume: ReturnType<typeof vi.fn<() => void>>;
  suspend: ReturnType<typeof vi.fn<() => void>>;
  stop: ReturnType<typeof vi.fn<() => void>>;
};

function harness(videoId = "vid-1") {
  const sockets: FakeSocket[] = [];
  const captures: FakeCapture[] = [];

  const socketFactory = (url: string, handlers: LiveSocketHandlers) => {
    const socket: FakeSocket = {
      url,
      handlers,
      send: vi.fn<(frame: ArrayBuffer) => void>(),
      close: vi.fn<() => void>(),
    };
    sockets.push(socket);
    return { send: socket.send, close: socket.close };
  };

  const captureFactory: AudioCaptureFactory = (_element, onFrame) => {
    const capture: FakeCapture = {
      onFrame,
      resume: vi.fn<() => void>(),
      suspend: vi.fn<() => void>(),
      stop: vi.fn<() => void>(),
    };
    captures.push(capture);
    return {
      resume: capture.resume,
      suspend: capture.suspend,
      stop: capture.stop,
    };
  };

  const state: { store?: PlaybackStore; analysis?: LiveAnalysis } = {};

  function Probe() {
    state.store = usePlaybackStore();
    state.analysis = useLiveAnalysis(videoId, { socketFactory, captureFactory });
    return null;
  }

  render(
    <PlaybackProvider>
      <Probe />
    </PlaybackProvider>,
  );

  // The player would register the media element; provide a stand-in so capture
  // can attach.
  act(() => {
    state.store!.registerMediaElement({} as HTMLMediaElement);
  });

  return {
    sockets,
    captures,
    store: () => state.store!,
    analysis: () => state.analysis!,
  };
}

const subtitleFrame = (id: string, start: number, text: string) =>
  JSON.stringify({ type: "subtitle", id, start, end: start + 1, text });

const resultFrame = (
  id: string,
  start: number,
  text: string,
  matches: unknown[] = [],
) =>
  JSON.stringify({
    type: "result",
    id,
    start,
    end: start + 1,
    text,
    matches,
  });

const interimFrame = (text: string) =>
  JSON.stringify({ type: "interim", text });

const play = (store: PlaybackStore) =>
  act(() => store.update({ paused: false }));
const pause = (store: PlaybackStore) =>
  act(() => store.update({ paused: true }));

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("useLiveAnalysis", () => {
  test("opens a socket on play and starts capture once it is open", () => {
    const h = harness();
    play(h.store());

    expect(h.sockets).toHaveLength(1);
    expect(h.sockets[0].url).toContain("/api/videos/vid-1/live");
    expect(h.captures).toHaveLength(0);

    act(() => h.sockets[0].handlers.onOpen());
    expect(h.captures).toHaveLength(1);
    expect(h.captures[0].resume).toHaveBeenCalledTimes(1);
    expect(h.analysis().status).toBe("live");
  });

  test("an imported video streams live: it opens the live socket on play and renders a streamed subtitle", () => {
    // VER-43 retired batch processing - an imported (uploaded/youtube/sample)
    // video now streams over the same live socket as a live stream. The hook is
    // source-agnostic: the video id is opaque, so an imported id opens the same
    // /api/videos/{id}/live socket and folds in subtitle frames identically.
    const h = harness("upload-vid-42");
    play(h.store());

    expect(h.sockets).toHaveLength(1);
    expect(h.sockets[0].url).toContain("/api/videos/upload-vid-42/live");

    act(() => h.sockets[0].handlers.onOpen());
    expect(h.analysis().status).toBe("live");

    act(() =>
      h.sockets[0].handlers.onFrame(
        subtitleFrame("0", 1, "the imported clip is streaming"),
      ),
    );

    const list = h.analysis().statements;
    expect(list).toHaveLength(1);
    expect(list[0]).toMatchObject({ text: "the imported clip is streaming" });
  });

  test("renders an incremental result aligned to the playback position", () => {
    const h = harness();
    act(() => h.store().update({ currentTime: 10 }));
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());

    act(() =>
      h.sockets[0].handlers.onFrame(
        resultFrame("0", 1.5, "the earth is round", [
          {
            kind: "claim",
            claim: "Earth is round",
            verdict: "corroborates",
            sources: [],
            similarity: 0.9,
          },
        ]),
      ),
    );

    const list = h.analysis().statements;
    expect(list).toHaveLength(1);
    // Stream-relative 1.5s is offset by the 10s base playback position.
    expect(list[0]).toMatchObject({
      start: 11.5,
      status: "checked",
      text: "the earth is round",
    });
  });

  test("a subtitle shows analysing, then its result resolves the statement", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());

    act(() => h.sockets[0].handlers.onFrame(subtitleFrame("0", 1, "claim one")));
    expect(h.analysis().statements[0]).toMatchObject({ status: "analysing" });

    act(() => h.sockets[0].handlers.onFrame(resultFrame("0", 1, "claim one")));
    expect(h.analysis().statements[0]).toMatchObject({ status: "checked" });
  });

  test("an interim frame surfaces a live caption that a subtitle then clears", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());

    act(() => h.sockets[0].handlers.onFrame(interimFrame("the earth is")));
    expect(h.analysis().caption).toBe("the earth is");
    // An interim caption is not a statement.
    expect(h.analysis().statements).toHaveLength(0);

    // Committing the utterance clears the caption; its text moves to the list.
    act(() =>
      h.sockets[0].handlers.onFrame(subtitleFrame("0", 1, "the earth is round")),
    );
    expect(h.analysis().caption).toBe("");
    expect(h.analysis().statements).toHaveLength(1);
  });

  test("seeking clears the in-flight live caption", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());
    act(() => h.sockets[0].handlers.onFrame(interimFrame("half a senten")));
    expect(h.analysis().caption).toBe("half a senten");

    act(() => h.store().notifySeeked());
    expect(h.analysis().caption).toBe("");
  });

  test("clears the live caption when the stream leaves the live state", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());
    act(() => h.sockets[0].handlers.onFrame(interimFrame("mid utteran")));
    expect(h.analysis().caption).toBe("mid utteran");

    // Pausing leaves the live state; the stale partial must not linger.
    pause(h.store());
    expect(h.analysis().status).not.toBe("live");
    expect(h.analysis().caption).toBe("");
  });

  test("ignores a trailing interim that arrives after leaving the live state", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());
    act(() => h.sockets[0].handlers.onFrame(interimFrame("mid utteran")));
    expect(h.analysis().caption).toBe("mid utteran");

    pause(h.store());
    expect(h.analysis().caption).toBe("");

    // Pause leaves the socket open; a late interim on it must NOT re-show the
    // caption under the now-paused panel.
    act(() => h.sockets[0].handlers.onFrame(interimFrame("late partial")));
    expect(h.analysis().caption).toBe("");
  });

  test("forwards captured PCM frames to the open socket", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());

    const frame = new ArrayBuffer(8);
    act(() => h.captures[0].onFrame(frame));
    expect(h.sockets[0].send).toHaveBeenCalledWith(frame);
  });

  test("pausing suspends capture and resuming continues it on the same socket", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());

    pause(h.store());
    expect(h.captures[0].suspend).toHaveBeenCalledTimes(1);
    expect(h.analysis().status).toBe("paused");

    play(h.store());
    expect(h.captures[0].resume).toHaveBeenCalledTimes(2); // start + resume
    expect(h.sockets).toHaveLength(1); // no new socket
  });

  test("seeking resets the session but preserves resolved verdicts", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());
    act(() => h.sockets[0].handlers.onFrame(resultFrame("0", 1, "checked claim")));
    act(() => h.sockets[0].handlers.onFrame(subtitleFrame("1", 5, "in flight")));
    expect(h.analysis().statements).toHaveLength(2);

    act(() => h.store().notifySeeked());

    // Old socket closed, in-flight statement dropped, verdict kept, new socket
    // opened.
    expect(h.sockets[0].close).toHaveBeenCalledTimes(1);
    expect(h.sockets).toHaveLength(2);
    const list = h.analysis().statements;
    expect(list).toHaveLength(1);
    expect(list[0]).toMatchObject({ status: "checked", text: "checked claim" });
  });

  test("ignores frames from a superseded socket after a seek", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());
    act(() => h.store().notifySeeked());

    // A late frame from the first, now-closed socket must not appear.
    act(() => h.sockets[0].handlers.onFrame(resultFrame("9", 2, "stale")));
    expect(h.analysis().statements).toHaveLength(0);
  });

  test("reconnects with backoff after an unclean drop", () => {
    vi.useFakeTimers();
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());

    act(() => h.sockets[0].handlers.onClose(false));
    expect(h.analysis().status).toBe("reconnecting");

    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(h.sockets).toHaveLength(2);
    act(() => h.sockets[1].handlers.onOpen());
    expect(h.analysis().status).toBe("live");
  });

  test("a clean close ends the stream without reconnecting", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());

    act(() => h.sockets[0].handlers.onClose(true));
    expect(h.analysis().status).toBe("ended");
    expect(h.sockets).toHaveLength(1);
  });

  test("exposes a running summary derived from the same statements", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());

    // Idle/empty: no statements, so every count is zero.
    expect(h.analysis().summary).toMatchObject({
      checked: 0,
      corroborates: 0,
      analysing: 0,
    });

    act(() => h.sockets[0].handlers.onFrame(subtitleFrame("0", 1, "claim one")));
    expect(h.analysis().summary).toMatchObject({ analysing: 1, checked: 0 });

    act(() =>
      h.sockets[0].handlers.onFrame(
        resultFrame("0", 1, "claim one", [
          {
            kind: "claim",
            claim: "Earth is round",
            verdict: "corroborates",
            sources: [],
            similarity: 0.9,
          },
        ]),
      ),
    );
    expect(h.analysis().summary).toMatchObject({
      analysing: 0,
      checked: 1,
      corroborates: 1,
    });
  });

  test("the summary keeps a stable identity across interim-only updates", () => {
    const h = harness();
    play(h.store());
    act(() => h.sockets[0].handlers.onOpen());
    act(() =>
      h.sockets[0].handlers.onFrame(resultFrame("0", 1, "checked claim")),
    );

    const before = h.analysis().summary;
    // An interim caption changes nothing in the statement set, so the memoized
    // summary must not be recomputed into a new object.
    act(() => h.sockets[0].handlers.onFrame(interimFrame("still talking")));
    expect(h.analysis().summary).toBe(before);
  });
});
