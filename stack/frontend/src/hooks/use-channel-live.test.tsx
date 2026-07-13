import { act, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { LiveSocketHandlers } from "@/lib/live/ports";
import { type LiveAnalysis, useChannelLive } from "./use-channel-live";

type FakeSocket = {
  url: string;
  handlers: LiveSocketHandlers;
  close: ReturnType<typeof vi.fn<() => void>>;
};

function harness(channelId = "chan-1") {
  const sockets: FakeSocket[] = [];
  const socketFactory = (url: string, handlers: LiveSocketHandlers) => {
    const socket: FakeSocket = { url, handlers, close: vi.fn<() => void>() };
    sockets.push(socket);
    return { send: vi.fn(), close: socket.close };
  };

  const state: { analysis?: LiveAnalysis } = {};
  function Probe() {
    state.analysis = useChannelLive(channelId, { socketFactory });
    return null;
  }
  render(<Probe />);
  return {
    sockets,
    analysis: () => state.analysis!,
    last: () => sockets[sockets.length - 1],
  };
}

const subtitleFrame = (id: string, text: string, speaker?: string) =>
  JSON.stringify({ type: "subtitle", id, start: 0, end: 1, text, speaker });

const claimsFrame = (unitId: string, claimId: string, text: string) =>
  JSON.stringify({
    type: "claims",
    id: unitId,
    claims: [{ claim_id: claimId, text, status: "pending" }],
  });

const speakerTallyFrame = (speaker: string, credible: number) =>
  JSON.stringify({
    type: "speaker_tally",
    speaker,
    credible,
    disputed: 0,
    unverifiable: 0,
  });

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("useChannelLive", () => {
  test("opens the channel viewer socket and reports connecting then live", () => {
    const h = harness("chan-7");
    expect(h.sockets).toHaveLength(1);
    expect(h.last().url).toContain("/api/tv/channels/chan-7/live");
    expect(h.analysis().status).toBe("connecting");

    act(() => h.last().handlers.onOpen());
    expect(h.analysis().status).toBe("live");
  });

  test("ingests a subtitle, its claims, and a speaker tally into the analysis", () => {
    const h = harness();
    act(() => h.last().handlers.onOpen());

    act(() => h.last().handlers.onFrame(subtitleFrame("u1", "the bridge opened in 1937", "A")));
    act(() => h.last().handlers.onFrame(claimsFrame("u1", "c1", "the bridge opened in 1937")));
    act(() => h.last().handlers.onFrame(speakerTallyFrame("A", 2)));

    const analysis = h.analysis();
    expect(analysis.statements).toHaveLength(1);
    expect(analysis.statements[0].text).toBe("the bridge opened in 1937");
    expect(analysis.claimsFor("u1")).toHaveLength(1);
    expect(analysis.claimsFor("u1")[0].text).toBe("the bridge opened in 1937");
    expect(analysis.speakers).toHaveLength(1);
    expect(analysis.speakers[0].speaker).toBe("A");
  });

  test("shows an interim caption then clears it once the statement commits", () => {
    const h = harness();
    act(() => h.last().handlers.onOpen());

    act(() =>
      h.last().handlers.onFrame(JSON.stringify({ type: "interim", text: "still speaking" })),
    );
    expect(h.analysis().caption).toBe("still speaking");

    act(() => h.last().handlers.onFrame(subtitleFrame("u1", "committed statement")));
    expect(h.analysis().caption).toBe("");
  });

  test("sets an ended status on an off_air frame and does not reconnect on the close that follows", () => {
    const h = harness();
    act(() => h.last().handlers.onOpen());
    act(() => h.last().handlers.onFrame(JSON.stringify({ type: "off_air" })));
    expect(h.analysis().status).toBe("ended");

    // The backend closes after off_air; that close must not trigger a reconnect.
    act(() => h.last().handlers.onClose(false));
    act(() => vi.runOnlyPendingTimers());
    expect(h.sockets).toHaveLength(1);
    expect(h.analysis().status).toBe("ended");
  });

  test("ignores a trailing interim after off_air (stays ended, no caption)", () => {
    const h = harness();
    act(() => h.last().handlers.onOpen());
    act(() => h.last().handlers.onFrame(JSON.stringify({ type: "off_air" })));
    // A late interim the backend may emit before the socket closes must not
    // repopulate the caption or leave the ended status.
    act(() =>
      h
        .last()
        .handlers.onFrame(JSON.stringify({ type: "interim", text: "late words" })),
    );
    expect(h.analysis().status).toBe("ended");
    expect(h.analysis().caption).toBe("");
  });

  test("ends on a clean close without reconnecting", () => {
    const h = harness();
    act(() => h.last().handlers.onOpen());
    act(() => h.last().handlers.onClose(true));
    act(() => vi.runOnlyPendingTimers());
    expect(h.sockets).toHaveLength(1);
    expect(h.analysis().status).toBe("ended");
  });

  test("reconnects with backoff on an unclean close", () => {
    const h = harness();
    act(() => h.last().handlers.onOpen());
    act(() => h.last().handlers.onClose(false));
    expect(h.analysis().status).toBe("reconnecting");

    act(() => vi.runOnlyPendingTimers());
    expect(h.sockets).toHaveLength(2);
    act(() => h.last().handlers.onOpen());
    expect(h.analysis().status).toBe("live");
  });
});
