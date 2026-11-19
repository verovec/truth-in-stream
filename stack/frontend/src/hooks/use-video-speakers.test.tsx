import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import type { SpeakerUpdate, VideoSpeaker } from "@/lib/video/speakers";
import { useVideoSpeakers } from "./use-video-speakers";

const speaker = (overrides: Partial<VideoSpeaker> = {}): VideoSpeaker => ({
  label: "A",
  name: "Marine Durand",
  role: "principal",
  confidence: 0.9,
  method: "signals",
  evidence: [],
  operatorSet: false,
  ...overrides,
});

type Props = {
  videoId: string | null;
  loaded: VideoSpeaker[];
  awaitNaming?: boolean;
};

function harness(
  initial: Props,
  seams: {
    update?: (
      videoId: string,
      label: string,
      update: SpeakerUpdate,
    ) => Promise<VideoSpeaker>;
    rerunNaming?: (videoId: string) => Promise<VideoSpeaker[]>;
    load?: (videoId: string, signal?: AbortSignal) => Promise<VideoSpeaker[]>;
  } = {},
) {
  return renderHook(
    (props: Props) =>
      useVideoSpeakers({ ...props, ...seams, pollIntervalMs: 5 }),
    { initialProps: initial },
  );
}

describe("useVideoSpeakers", () => {
  test("starts from the loaded mapping and resets on a new video or a fresh read", () => {
    const first = [speaker()];
    const h = harness({ videoId: "v1", loaded: first });
    expect(h.result.current.speakers).toBe(first);

    const second = [speaker({ label: "B", name: "Paul Lefèvre" })];
    h.rerender({ videoId: "v2", loaded: second });
    expect(h.result.current.speakers).toBe(second);

    const refreshed = [speaker({ name: "Refreshed" })];
    h.rerender({ videoId: "v2", loaded: refreshed });
    expect(h.result.current.speakers).toBe(refreshed);
  });

  test("save replaces the speaker in place and tracks the label in flight", async () => {
    let resolve: (value: VideoSpeaker) => void = () => {};
    const update = vi.fn(
      () =>
        new Promise<VideoSpeaker>((res) => {
          resolve = res;
        }),
    );
    const h = harness(
      { videoId: "v1", loaded: [speaker(), speaker({ label: "B", name: "" })] },
      { update },
    );

    act(() => h.result.current.save("B", { name: "Paul Lefèvre", role: "guest" }));
    expect([...h.result.current.savingLabels]).toEqual(["B"]);
    expect(update).toHaveBeenCalledWith("v1", "B", {
      name: "Paul Lefèvre",
      role: "guest",
    });

    await act(async () => {
      resolve(
        speaker({ label: "B", name: "Paul Lefèvre", role: "guest", operatorSet: true }),
      );
    });
    expect(h.result.current.savingLabels.size).toBe(0);
    expect(h.result.current.speakers.map((s) => s.name)).toEqual([
      "Marine Durand",
      "Paul Lefèvre",
    ]);
    expect(h.result.current.speakers[1]?.operatorSet).toBe(true);
  });

  test("a failed save keeps the mapping and reports the error, cleared by the next attempt", async () => {
    const update = vi
      .fn<(videoId: string, label: string, update: SpeakerUpdate) => Promise<VideoSpeaker>>()
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce(speaker({ name: "Sophie Garnier" }));
    const loaded = [speaker()];
    const h = harness({ videoId: "v1", loaded }, { update });

    act(() => h.result.current.save("A", { name: "x", role: "guest" }));
    await waitFor(() => expect(h.result.current.error).toBe("save"));
    expect(h.result.current.speakers).toBe(loaded);
    expect(h.result.current.savingLabels.size).toBe(0);

    act(() => h.result.current.save("A", { name: "Sophie Garnier", role: "principal" }));
    expect(h.result.current.error).toBeNull();
    await waitFor(() =>
      expect(h.result.current.speakers[0]?.name).toBe("Sophie Garnier"),
    );
  });

  test("two saves in flight are tracked per label, each settling on its own response", async () => {
    const resolvers = new Map<string, (value: VideoSpeaker) => void>();
    const update = vi.fn(
      (_videoId: string, label: string) =>
        new Promise<VideoSpeaker>((res) => {
          resolvers.set(label, res);
        }),
    );
    const h = harness(
      { videoId: "v1", loaded: [speaker(), speaker({ label: "B", name: "" })] },
      { update },
    );

    act(() => {
      h.result.current.save("A", { name: "One", role: "guest" });
      h.result.current.save("B", { name: "Two", role: "guest" });
    });
    expect([...h.result.current.savingLabels].sort()).toEqual(["A", "B"]);

    await act(async () => {
      resolvers.get("A")?.(speaker({ name: "One" }));
    });
    expect([...h.result.current.savingLabels]).toEqual(["B"]);

    await act(async () => {
      resolvers.get("B")?.(speaker({ label: "B", name: "Two" }));
    });
    expect(h.result.current.savingLabels.size).toBe(0);
  });

  test("after a watched completion it re-reads the mapping until the naming step has written it", async () => {
    const named = [speaker()];
    const load = vi
      .fn<(videoId: string, signal?: AbortSignal) => Promise<VideoSpeaker[]>>()
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(new Error("transient"))
      .mockResolvedValue(named);
    const h = harness({ videoId: "v1", loaded: [], awaitNaming: true }, { load });

    await waitFor(() => expect(h.result.current.speakers).toBe(named));
    expect(load).toHaveBeenCalledTimes(3);
    expect(load).toHaveBeenCalledWith("v1", expect.any(AbortSignal));

    // Once the proposals are in, the follow-up read stops.
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(load).toHaveBeenCalledTimes(3);
  });

  test("operator-set rows alone do not end the wait: the step's own proposals do", async () => {
    const operatorOnly = [speaker({ operatorSet: true, method: "operator" })];
    const withProposals = [...operatorOnly, speaker({ label: "B", name: "" })];
    const load = vi
      .fn<(videoId: string, signal?: AbortSignal) => Promise<VideoSpeaker[]>>()
      .mockResolvedValueOnce(operatorOnly)
      .mockResolvedValue(withProposals);
    const h = harness(
      { videoId: "v1", loaded: operatorOnly, awaitNaming: true },
      { load },
    );

    await waitFor(() => expect(h.result.current.speakers).toBe(withProposals));
    expect(load).toHaveBeenCalledTimes(2);
  });

  test("does not re-read a mapping that was not awaited or is already there", async () => {
    const load = vi.fn(async () => [speaker()]);
    harness({ videoId: "v1", loaded: [] }, { load });
    harness({ videoId: "v2", loaded: [speaker()], awaitNaming: true }, { load });

    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(load).not.toHaveBeenCalled();
  });

  test("rerun replaces the whole mapping", async () => {
    const next = [speaker({ name: "" }), speaker({ label: "B", name: "Paul Lefèvre" })];
    const rerunNaming = vi.fn(async () => next);
    const h = harness({ videoId: "v1", loaded: [speaker()] }, { rerunNaming });

    act(() => h.result.current.rerun());
    expect(h.result.current.rerunning).toBe(true);
    await waitFor(() => expect(h.result.current.rerunning).toBe(false));
    expect(rerunNaming).toHaveBeenCalledWith("v1");
    expect(h.result.current.speakers).toBe(next);
  });

  test("a failed rerun keeps the previous names and reports the error", async () => {
    const rerunNaming = vi.fn(async () => {
      throw new Error("boom");
    });
    const loaded = [speaker()];
    const h = harness({ videoId: "v1", loaded }, { rerunNaming });

    act(() => h.result.current.rerun());
    await waitFor(() => expect(h.result.current.error).toBe("rerun"));
    expect(h.result.current.speakers).toBe(loaded);
    expect(h.result.current.rerunning).toBe(false);
  });

  test("a response landing after the video changed is dropped", async () => {
    let resolve: (value: VideoSpeaker[]) => void = () => {};
    const rerunNaming = vi.fn(
      () =>
        new Promise<VideoSpeaker[]>((res) => {
          resolve = res;
        }),
    );
    const h = harness({ videoId: "v1", loaded: [speaker()] }, { rerunNaming });
    act(() => h.result.current.rerun());

    const other = [speaker({ label: "Z", name: "Other Video" })];
    h.rerender({ videoId: "v2", loaded: other });
    await act(async () => {
      resolve([speaker({ name: "Stale" })]);
    });

    expect(h.result.current.speakers).toBe(other);
    expect(h.result.current.rerunning).toBe(false);
  });

  test("does nothing without a video", () => {
    const update = vi.fn();
    const rerunNaming = vi.fn();
    const h = harness({ videoId: null, loaded: [] }, { update, rerunNaming });

    act(() => {
      h.result.current.save("A", { name: "x", role: "guest" });
      h.result.current.rerun();
    });
    expect(update).not.toHaveBeenCalled();
    expect(rerunNaming).not.toHaveBeenCalled();
  });
});
