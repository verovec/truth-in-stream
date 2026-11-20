import { describe, expect, test } from "vitest";
import type { LiveClaim } from "./claims";
import type { LiveStatement } from "./statements";
import {
  laneCount,
  markerVerdict,
  MIN_MARKER_WIDTH,
  timelineMarkers,
} from "./timeline";

function statement(
  id: string,
  start: number,
  end: number,
): LiveStatement {
  return { id, start, end, text: `statement ${id}`, status: "analysing" };
}

function claim(
  claimId: string,
  overrides: Partial<LiveClaim> = {},
): LiveClaim {
  return {
    claimId,
    text: `claim ${claimId}`,
    status: "verified",
    verdict: "credible",
    ...overrides,
  };
}

// claimsMap builds the claimsFor lookup the hydrated snapshot exposes.
function claimsMap(
  entries: Record<string, LiveClaim[]>,
): (statementId: string) => LiveClaim[] {
  return (statementId) => entries[statementId] ?? [];
}

describe("markerVerdict", () => {
  test("a concrete verdict marks the timeline whatever the status", () => {
    expect(markerVerdict(claim("c1", { verdict: "disputed" }))).toBe("disputed");
  });

  test("a verified claim with a degenerate missing verdict reads unverifiable", () => {
    expect(markerVerdict(claim("c1", { verdict: undefined }))).toBe(
      "unverifiable",
    );
  });

  test.each(["pending", "checking", "unchecked", "error"] as const)(
    "a %s claim without a verdict leaves no mark",
    (status) => {
      expect(
        markerVerdict(claim("c1", { status, verdict: undefined })),
      ).toBeNull();
    },
  );
});

describe("timelineMarkers", () => {
  test("positions a marker by its statement span as fractions of the duration", () => {
    const markers = timelineMarkers(
      [statement("s1", 10, 30)],
      claimsMap({ s1: [claim("c1")] }),
      100,
    );
    expect(markers).toHaveLength(1);
    expect(markers[0]).toMatchObject({
      claimId: "c1",
      statementId: "s1",
      verdict: "credible",
      seekTo: 10,
      left: 0.1,
      width: 0.2,
      lane: 0,
    });
  });

  test("returns no markers until the duration is known", () => {
    const statements = [statement("s1", 10, 30)];
    const claims = claimsMap({ s1: [claim("c1")] });
    expect(timelineMarkers(statements, claims, 0)).toEqual([]);
    expect(timelineMarkers(statements, claims, Number.NaN)).toEqual([]);
    expect(timelineMarkers(statements, claims, Number.POSITIVE_INFINITY)).toEqual(
      [],
    );
  });

  test("skips claims that were never checked", () => {
    const markers = timelineMarkers(
      [statement("s1", 10, 30)],
      claimsMap({
        s1: [
          claim("c1", { status: "pending", verdict: undefined }),
          claim("c2", { status: "checking", verdict: undefined }),
          claim("c3", { status: "unchecked", verdict: undefined }),
          claim("c4", { status: "error", verdict: undefined }),
          claim("c5", { verdict: "disputed" }),
        ],
      }),
      100,
    );
    expect(markers.map((marker) => marker.claimId)).toEqual(["c5"]);
  });

  test("a statement with no claims leaves no markers", () => {
    expect(
      timelineMarkers([statement("s1", 10, 30)], claimsMap({}), 100),
    ).toEqual([]);
  });

  test("a brief statement still gets a clickable minimum-width marker", () => {
    const markers = timelineMarkers(
      [statement("s1", 50, 50.1)],
      claimsMap({ s1: [claim("c1")] }),
      1000,
    );
    expect(markers[0].width).toBe(MIN_MARKER_WIDTH);
  });

  test("clamps a span straddling the edges into the video", () => {
    const markers = timelineMarkers(
      [statement("s1", -5, 5), statement("s2", 95, 120)],
      claimsMap({ s1: [claim("c1")], s2: [claim("c2")] }),
      100,
    );
    expect(markers).toHaveLength(2);
    expect(markers[0]).toMatchObject({ claimId: "c1", seekTo: 0, left: 0 });
    expect(markers[0].width).toBeCloseTo(0.05);
    const last = markers[1];
    expect(last.seekTo).toBe(95);
    expect(last.width).toBeCloseTo(0.05);
    expect(last.left + last.width).toBeLessThanOrEqual(1);
  });

  test("a minimum-width marker at the very end never overflows the strip", () => {
    const markers = timelineMarkers(
      [statement("s1", 999.5, 1000)],
      claimsMap({ s1: [claim("c1")] }),
      1000,
    );
    expect(markers[0].width).toBe(MIN_MARKER_WIDTH);
    expect(markers[0].left + markers[0].width).toBeLessThanOrEqual(1);
  });

  test("drops spans entirely outside the video", () => {
    const markers = timelineMarkers(
      [statement("s1", 120, 130), statement("s2", -10, -1)],
      claimsMap({ s1: [claim("c1")], s2: [claim("c2")] }),
      100,
    );
    expect(markers).toEqual([]);
  });

  test("stacks overlapping markers into distinct lanes, each keeping its own target", () => {
    const markers = timelineMarkers(
      [statement("s1", 10, 30)],
      claimsMap({
        s1: [claim("c1"), claim("c2", { verdict: "disputed" })],
      }),
      100,
    );
    expect(markers).toHaveLength(2);
    expect(new Set(markers.map((marker) => marker.lane))).toEqual(
      new Set([0, 1]),
    );
    expect(laneCount(markers)).toBe(2);
  });

  test("disjoint markers reuse the first lane", () => {
    const markers = timelineMarkers(
      [statement("s1", 0, 10), statement("s2", 20, 30), statement("s3", 40, 50)],
      claimsMap({
        s1: [claim("c1")],
        s2: [claim("c2")],
        s3: [claim("c3")],
      }),
      100,
    );
    expect(markers.map((marker) => marker.lane)).toEqual([0, 0, 0]);
    expect(laneCount(markers)).toBe(1);
  });

  test("markers come back ordered by position for a chronological tab order", () => {
    const markers = timelineMarkers(
      [statement("s2", 40, 50), statement("s1", 0, 10)],
      claimsMap({ s1: [claim("c1")], s2: [claim("c2")] }),
      100,
    );
    expect(markers.map((marker) => marker.claimId)).toEqual(["c1", "c2"]);
  });

  test("laneCount of no markers is zero", () => {
    expect(laneCount([])).toBe(0);
  });
});
