import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import {
  PlaybackProvider,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import { type LiveAnalysis, useLiveAnalysis } from "@/hooks/use-live-analysis";
import type { PlaybackStore } from "@/lib/playback/playback-store";
import type { AudioCaptureFactory, LiveSocketHandlers } from "./ports";
import { type LiveFrame, parseLiveFrameValue } from "./frames";
import { foldAnalysisFrames, hydrateAnalysis } from "./hydrate";
import type { LiveClaim } from "./claims";

// SESSION is one stored pre-analysis session in wire shape: the exact frames a
// live WebSocket would have sent, with absolute video-time timestamps. It mixes
// the verify path (claims + claim_result + speaker_tally), a legacy result, a
// consistency flag, and an interim caption that hydration must skip.
const SESSION: Record<string, unknown>[] = [
  { type: "interim", text: "unemployment fell by" },
  {
    type: "subtitle",
    id: "s1",
    start: 5,
    end: 8,
    text: "unemployment fell by half since 2020",
    speaker: "A",
  },
  {
    type: "claims",
    id: "s1",
    claims: [
      {
        claim_id: "c1",
        text: "Unemployment fell by half since 2020",
        status: "pending",
        quote: "unemployment fell by half",
        spans: [{ segment_id: "s1", start: 0, end: 25 }],
      },
    ],
  },
  {
    type: "claim_result",
    id: "s1",
    claim_id: "c1",
    status: "verified",
    source: "verified",
    verdict: "disputed",
    basis: "evidence",
    confidence: 0.82,
    rationale: "INSEE series shows a one-fifth drop, not half.",
    source_label: "INSEE",
    source_url: "https://insee.fr/series",
  },
  { type: "speaker_tally", speaker: "A", credible: 0, disputed: 1, unverifiable: 0 },
  {
    type: "subtitle",
    id: "s2",
    start: 9,
    end: 11,
    text: "and the weather is nice today",
    speaker: "A",
  },
  {
    type: "result",
    id: "s2",
    start: 9,
    end: 11,
    text: "and the weather is nice today",
    matches: [],
    skip_reason: "not_a_claim",
  },
  {
    type: "consistency",
    id: "s2",
    earlier_id: "s1",
    earlier_text: "unemployment fell by half since 2020",
    speaker: "A",
    rationale: "contradicts the earlier framing",
  },
];

function sessionFrames(): LiveFrame[] {
  return SESSION.map(parseLiveFrameValue).filter(
    (frame): frame is LiveFrame => frame !== null,
  );
}

// stripSeq removes the per-session id namespace the live hook prefixes
// ("1:s1" -> "s1"), so hydrated state (which keeps the stored ids verbatim)
// compares structurally to a live session's.
const stripSeq = (id: string) => id.replace(/^\d+:/, "");

function normalizeLiveClaims(claims: LiveClaim[]): LiveClaim[] {
  return claims.map((claim) =>
    claim.spans
      ? {
          ...claim,
          spans: claim.spans.map((span) => ({
            ...span,
            segmentId: stripSeq(span.segmentId),
          })),
        }
      : claim,
  );
}

// liveHarness runs the real live hook against a fake socket opened at playback
// position 0 (base time 0, the same clock a stored session uses), so the frames
// fold through the exact live ingest path.
function liveHarness() {
  const handlersRef: { current: LiveSocketHandlers | null } = { current: null };
  const socketFactory = (_url: string, handlers: LiveSocketHandlers) => {
    handlersRef.current = handlers;
    return { send: vi.fn(), close: vi.fn() };
  };
  const captureFactory: AudioCaptureFactory = () => ({
    resume: vi.fn(),
    suspend: vi.fn(),
    stop: vi.fn(),
  });

  const state: { store?: PlaybackStore; analysis?: LiveAnalysis } = {};
  function Probe() {
    state.store = usePlaybackStore();
    state.analysis = useLiveAnalysis("vid-1", { socketFactory, captureFactory });
    return null;
  }
  render(
    <PlaybackProvider>
      <Probe />
    </PlaybackProvider>,
  );
  act(() => {
    state.store!.registerMediaElement({} as HTMLMediaElement);
    state.store!.update({ paused: false });
  });
  act(() => handlersRef.current!.onOpen());
  for (const frame of SESSION) {
    act(() => handlersRef.current!.onFrame(JSON.stringify(frame)));
  }
  return state.analysis!;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("foldAnalysisFrames", () => {
  test("folds a stored session into statements, claims, and speakers with absolute times", () => {
    const { statements, claims, speakers } = foldAnalysisFrames(sessionFrames());

    expect([...statements.byId.keys()]).toEqual(["s1", "s2"]);
    const s1 = statements.byId.get("s1")!;
    expect(s1).toMatchObject({ start: 5, end: 8, speaker: "A" });
    const s2 = statements.byId.get("s2")!;
    expect(s2).toMatchObject({
      status: "checked",
      skipReason: "not_a_claim",
      inconsistency: { earlierId: "s1" },
    });
    expect(claims.byUnit.get("s1")?.get("c1")).toMatchObject({
      status: "verified",
      verdict: "disputed",
      sourceLabel: "INSEE",
    });
    expect(speakers.get("A")).toMatchObject({ disputed: 1 });
  });

  test("skips interim captions: they are transient by definition", () => {
    const onlyInterim = sessionFrames().filter((f) => f.type === "interim");
    expect(onlyInterim).toHaveLength(1);
    const state = foldAnalysisFrames(onlyInterim);
    expect(state.statements.byId.size).toBe(0);
    expect(state.claims.byUnit.size).toBe(0);
    expect(state.speakers.size).toBe(0);
  });
});

describe("hydrateAnalysis", () => {
  test("projects a finished snapshot: full transcript, empty caption, ended status", () => {
    const snapshot = hydrateAnalysis(sessionFrames());

    expect(snapshot.status).toBe("ended");
    expect(snapshot.caption).toBe("");
    expect(snapshot.statements.map((s) => s.id)).toEqual(["s1", "s2"]);
    expect(snapshot.statements[0].start).toBe(5);
    expect(snapshot.claimsFor("s1")).toHaveLength(1);
    expect(snapshot.highlightsFor("s1")).toHaveLength(1);
    expect(snapshot.highlightsFor("s2")).toHaveLength(0);
    expect(snapshot.speakers).toHaveLength(1);
  });

  test("produces the same analysis state as the live WebSocket session it was recorded from", () => {
    const live = liveHarness();
    const hydrated = hydrateAnalysis(sessionFrames());

    // Statements match one-for-one once the live session's id namespace is
    // stripped; timestamps are identical because the socket opened at position
    // 0, the same base a stored session is recorded from.
    const liveStatements = live.statements.map((statement) => ({
      ...statement,
      id: stripSeq(statement.id),
      inconsistency: statement.inconsistency
        ? {
            ...statement.inconsistency,
            earlierId: stripSeq(statement.inconsistency.earlierId),
          }
        : undefined,
    }));
    expect(hydrated.statements).toEqual(liveStatements);

    expect(hydrated.summary).toEqual(live.summary);
    expect(hydrated.speakers).toEqual(live.speakers);

    for (const statement of live.statements) {
      expect(hydrated.claimsFor(stripSeq(statement.id))).toEqual(
        normalizeLiveClaims(live.claimsFor(statement.id)),
      );
      expect(hydrated.highlightsFor(stripSeq(statement.id))).toEqual(
        live
          .highlightsFor(statement.id)
          .map((highlight) => ({
            ...highlight,
            unitId: stripSeq(highlight.unitId),
          })),
      );
    }
  });
});
