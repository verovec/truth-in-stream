import { afterEach, describe, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/http";
import { json, stubBackend } from "@/test/fact-check";
import { getVideoAnalysis } from "./analysis";
import {
  getVideoSpeakers,
  parseVideoSpeakers,
  rerunSpeakerNaming,
  speakerNamesByLabel,
  updateVideoSpeaker,
} from "./speakers";

afterEach(() => {
  vi.restoreAllMocks();
});

const durandWire = {
  label: "A",
  name: "Marine Durand",
  role: "principal",
  confidence: 0.92,
  method: "signals",
  operator_set: false,
  evidence: [
    { kind: "handoff", quote: "Madame Durand, vous avez deux minutes.", at: 90.5 },
  ],
};

describe("parseVideoSpeakers", () => {
  test("normalizes a wire mapping", () => {
    expect(parseVideoSpeakers([durandWire])).toEqual([
      {
        label: "A",
        name: "Marine Durand",
        role: "principal",
        confidence: 0.92,
        method: "signals",
        operatorSet: false,
        evidence: [
          {
            kind: "handoff",
            quote: "Madame Durand, vous avez deux minutes.",
            at: 90.5,
          },
        ],
      },
    ]);
  });

  test("drops entries without a label and degrades unknown fields", () => {
    const speakers = parseVideoSpeakers([
      { name: "No Label" },
      "not-an-object",
      {
        label: "B",
        role: "host",
        method: "guess",
        confidence: "high",
        evidence: [{ kind: "handoff" }, { quote: "kept", at: "soon" }],
        operator_set: true,
      },
    ]);

    expect(speakers).toEqual([
      {
        label: "B",
        name: "",
        role: "unknown",
        confidence: 0,
        method: "none",
        operatorSet: true,
        evidence: [{ kind: "", quote: "kept", at: 0 }],
      },
    ]);
  });

  test("treats anything that is not a list as an absent mapping", () => {
    expect(parseVideoSpeakers(undefined)).toEqual([]);
    expect(parseVideoSpeakers({ speakers: [] })).toEqual([]);
  });
});

describe("speakerNamesByLabel", () => {
  test("keeps named labels only, so unnamed voices fall back to their letter", () => {
    const speakers = parseVideoSpeakers([
      durandWire,
      { label: "B", name: "", role: "moderator" },
    ]);
    expect(speakerNamesByLabel(speakers)).toEqual({ A: "Marine Durand" });
  });
});

describe("getVideoAnalysis speakers", () => {
  test("carries the embedded mapping, empty when the backend sends none", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/analysis"),
        responses: [
          json(200, {
            analysis_status: "complete",
            frames: [],
            speakers: [durandWire],
          }),
          json(200, { analysis_status: "complete", frames: [] }),
        ],
      },
    ]);

    const named = await getVideoAnalysis("vid-1");
    expect(named.speakers.map((speaker) => speaker.name)).toEqual([
      "Marine Durand",
    ]);

    const unnamed = await getVideoAnalysis("vid-1");
    expect(unnamed.speakers).toEqual([]);
  });
});

describe("getVideoSpeakers", () => {
  test("reads the stored mapping", async () => {
    stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/videos/vid-1/speakers") &&
          (init?.method ?? "GET") === "GET",
        responses: [json(200, { speakers: [durandWire] }), json(200, {})],
      },
    ]);

    expect((await getVideoSpeakers("vid-1")).map((s) => s.name)).toEqual([
      "Marine Durand",
    ]);
    expect(await getVideoSpeakers("vid-1")).toEqual([]);
  });

  test("surfaces a backend refusal as an ApiError", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/speakers"),
        responses: [json(401, { error: "unauthorized" })],
      },
    ]);

    await expect(getVideoSpeakers("vid-1")).rejects.toBeInstanceOf(ApiError);
  });
});

describe("updateVideoSpeaker", () => {
  test("PATCHes the label with the name and role and returns the saved speaker", async () => {
    const fetchSpy = stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/videos/vid-1/speakers/A") &&
          init?.method === "PATCH",
        responses: [
          json(200, {
            ...durandWire,
            name: "Sophie Garnier",
            role: "guest",
            method: "operator",
            operator_set: true,
          }),
        ],
      },
    ]);

    const saved = await updateVideoSpeaker("vid-1", "A", {
      name: "Sophie Garnier",
      role: "guest",
    });

    expect(saved).toMatchObject({
      label: "A",
      name: "Sophie Garnier",
      role: "guest",
      operatorSet: true,
    });
    const init = fetchSpy.mock.calls[0]?.[1];
    expect(JSON.parse(String(init?.body))).toEqual({
      name: "Sophie Garnier",
      role: "guest",
    });
  });

  test("encodes the label and surfaces a backend refusal as an ApiError", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/speakers/A%2FB"),
        responses: [json(403, { error: "admin role required" })],
      },
    ]);

    await expect(
      updateVideoSpeaker("vid-1", "A/B", { name: "x", role: "guest" }),
    ).rejects.toBeInstanceOf(ApiError);
  });

  test("rejects a malformed speaker in the response", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/speakers/A"),
        responses: [json(200, { name: "no label" })],
      },
    ]);

    await expect(
      updateVideoSpeaker("vid-1", "A", { name: "x", role: "guest" }),
    ).rejects.toThrow(/malformed speaker/);
  });
});

describe("rerunSpeakerNaming", () => {
  test("POSTs the re-run and returns the new mapping", async () => {
    stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/videos/vid-1/speakers/name") &&
          init?.method === "POST",
        responses: [json(200, { speakers: [durandWire] })],
      },
    ]);

    const speakers = await rerunSpeakerNaming("vid-1");
    expect(speakers.map((speaker) => speaker.label)).toEqual(["A"]);
  });

  test("surfaces a failed step as an ApiError", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/speakers/name"),
        responses: [json(502, { error: "speaker naming failed" })],
      },
    ]);

    await expect(rerunSpeakerNaming("vid-1")).rejects.toBeInstanceOf(ApiError);
  });
});
