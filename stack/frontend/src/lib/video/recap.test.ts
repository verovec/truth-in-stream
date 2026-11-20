import { afterEach, describe, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/http";
import { json, stubBackend } from "@/test/fact-check";
import { counts, recapSpeaker } from "@/test/recap";
import {
  addCounts,
  formatDuration,
  getVideoRecap,
  moveSheet,
  orderSheets,
  parseRecap,
  parseSheetOrder,
  serializeSheetOrder,
  truncateQuote,
  unverifiedShare,
} from "./recap";

afterEach(() => {
  vi.restoreAllMocks();
});

const wire = {
  video_id: "vid-1",
  title: "Le grand débat",
  analyzed_at: "2026-11-19T20:00:00Z",
  duration: 10260,
  speakers: [
    {
      label: "A",
      name: "Marine Durand",
      role: "principal",
      name_confirmed: true,
      speaking_time: 61.5,
      turns: 2,
      share: 1,
      counts: { detected: 3, credible: 1, disputed: 1, unverifiable: 0, not_verified: 1 },
      disputed: [
        {
          claim_id: "c1",
          at: 12.5,
          verdict: "disputed",
          flags: ["cherry-picked", 7, "cherry-picked"],
          quote: "Les salaires ont doublé",
          rationale: "Faux.",
          sources: [
            { title: "INSEE", url: "https://example.org/insee" },
            { title: "Piège", url: "javascript:alert(1)" },
            { title: "", url: "http://example.org/sans-titre" },
            { title: "INSEE (doublon)", url: "https://example.org/insee" },
            "pas un objet",
          ],
        },
      ],
      credible: [],
    },
    { name: "sans label" },
    { label: "B", role: "host" },
  ],
  principals: { speaking_time: 61.5, turns: 2, counts: { detected: 3, credible: 1, disputed: 1, not_verified: 1 } },
  others: {},
};

describe("parseRecap", () => {
  test("normalizes the wire recap", () => {
    const recap = parseRecap(wire);

    expect(recap).toMatchObject({
      videoId: "vid-1",
      title: "Le grand débat",
      analyzedAt: "2026-11-19T20:00:00Z",
      duration: 10260,
      principals: {
        speakingTime: 61.5,
        turns: 2,
        counts: counts({ detected: 3, credible: 1, disputed: 1, notVerified: 1 }),
      },
      others: { speakingTime: 0, turns: 0, counts: counts() },
    });
    expect(recap.speakers[0]).toMatchObject({
      label: "A",
      name: "Marine Durand",
      role: "principal",
      nameConfirmed: true,
      speakingTime: 61.5,
      share: 1,
      counts: counts({ detected: 3, credible: 1, disputed: 1, notVerified: 1 }),
    });
  });

  test("drops speakers without a label and degrades unknown roles", () => {
    const recap = parseRecap(wire);
    expect(recap.speakers.map((speaker) => speaker.label)).toEqual(["A", "B"]);
    expect(recap.speakers[1]).toMatchObject({
      name: "",
      role: "unknown",
      counts: counts(),
      disputed: [],
      credible: [],
    });
  });

  test("a malformed analysis date reads as unknown instead of breaking the page", () => {
    expect(parseRecap({ ...wire, analyzed_at: "unknown" }).analyzedAt).toBeNull();
    expect(parseRecap({ ...wire, analyzed_at: 42 }).analyzedAt).toBeNull();
  });

  test("keeps only distinct http(s) sources and distinct flags, so no script URL can reach a link", () => {
    const [statement] = parseRecap(wire).speakers[0].disputed;
    expect(statement.flags).toEqual(["cherry-picked"]);
    expect(statement.sources).toEqual([
      { title: "INSEE", url: "https://example.org/insee" },
      { title: "http://example.org/sans-titre", url: "http://example.org/sans-titre" },
    ]);
  });

  test("a malformed payload yields an empty report rather than a crash", () => {
    expect(parseRecap("nope")).toEqual({
      videoId: "",
      title: "",
      analyzedAt: null,
      duration: 0,
      speakers: [],
      principals: { speakingTime: 0, turns: 0, counts: counts() },
      others: { speakingTime: 0, turns: 0, counts: counts() },
    });
  });
});

describe("getVideoRecap", () => {
  test("fetches and parses the recap", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid%2F1/recap"),
        responses: [json(200, wire)],
      },
    ]);
    const recap = await getVideoRecap("vid/1");
    expect(recap.title).toBe("Le grand débat");
  });

  test("surfaces 409 and 404 as ApiErrors carrying the status", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/recap"),
        responses: [json(409, { error: "no completed analysis" })],
      },
    ]);
    const err = await getVideoRecap("vid-1").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(409);
  });
});

describe("counts helpers", () => {
  test("addCounts sums every outcome", () => {
    expect(
      addCounts(
        counts({ detected: 8, credible: 2, disputed: 3, unverifiable: 1, notVerified: 2 }),
        counts({ detected: 1, credible: 1 }),
      ),
    ).toEqual(counts({ detected: 9, credible: 3, disputed: 3, unverifiable: 1, notVerified: 2 }));
  });

  test("unverifiedShare counts unverifiable and not verified together", () => {
    expect(unverifiedShare(counts({ detected: 8, unverifiable: 1, notVerified: 3 }))).toBe(0.5);
    expect(unverifiedShare(counts())).toBe(0);
  });
});

describe("sheet order", () => {
  const speakers = [
    recapSpeaker({ label: "A" }),
    recapSpeaker({ label: "F", role: "moderator" }),
    recapSpeaker({ label: "B" }),
    recapSpeaker({ label: "C" }),
  ];
  const labels = (order: string[]) =>
    orderSheets(speakers, order).map((speaker) => speaker.label);

  test("defaults to the recap's own order, principals only", () => {
    expect(labels([])).toEqual(["A", "B", "C"]);
  });

  test("applies a requested order", () => {
    expect(labels(["C", "A", "B"])).toEqual(["C", "A", "B"]);
  });

  test("a stale or partial order never hides a sheet", () => {
    expect(labels(["C"])).toEqual(["C", "A", "B"]);
    expect(labels(["Z", "F", "B", "B"])).toEqual(["B", "A", "C"]);
  });

  test("round-trips through the URL parameter", () => {
    expect(parseSheetOrder(serializeSheetOrder(["C", "A", "B"]))).toEqual(["C", "A", "B"]);
    expect(parseSheetOrder(null)).toEqual([]);
    expect(parseSheetOrder("")).toEqual([]);
    expect(parseSheetOrder("A,,B")).toEqual(["A", "B"]);
  });

  test("moveSheet swaps neighbours and ignores out-of-bounds moves", () => {
    expect(moveSheet(["A", "B", "C"], 0, 1)).toEqual(["B", "A", "C"]);
    expect(moveSheet(["A", "B", "C"], 2, -1)).toEqual(["A", "C", "B"]);
    const same = ["A", "B", "C"];
    expect(moveSheet(same, 0, -1)).toBe(same);
    expect(moveSheet(same, 2, 1)).toBe(same);
    expect(moveSheet(same, 5, -1)).toBe(same);
  });
});

describe("truncateQuote", () => {
  test("leaves a short quote untouched", () => {
    expect(truncateQuote("  Le chômage a baissé.  ", 40)).toBe("Le chômage a baissé.");
  });

  test("cuts on a word boundary with an ellipsis and never rewrites words", () => {
    const quote = "Le chômage a baissé de deux points en un an, selon les chiffres.";
    const cut = truncateQuote(quote, 30);
    expect(cut).toBe("Le chômage a baissé de deux…");
    expect(quote.startsWith(cut.slice(0, -1))).toBe(true);
  });

  test("drops trailing punctuation before the ellipsis", () => {
    expect(truncateQuote("Un an, selon les chiffres publiés", 7)).toBe("Un an…");
  });

  test("hard-cuts a single word longer than the limit", () => {
    expect(truncateQuote("anticonstitutionnellement", 5)).toBe("antic…");
  });
});

describe("formatDuration", () => {
  test.each([
    [10_260, "2 h 51"],
    [3_600, "1 h 00"],
    [1_680, "28 min"],
    [45, "45 s"],
    [0, "0 s"],
    [Number.NaN, "0 s"],
    [-5, "0 s"],
  ])("%d seconds reads %s", (seconds, expected) => {
    expect(formatDuration(seconds)).toBe(expected);
  });
});
