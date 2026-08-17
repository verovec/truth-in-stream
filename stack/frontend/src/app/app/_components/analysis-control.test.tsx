import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import type { VideoAnalysisTrack } from "@/hooks/use-video-analysis";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import type { AnalysedLibraryVideo } from "@/lib/video/analysis";
import { AnalysisControl } from "./analysis-control";

function video(
  overrides: Partial<AnalysedLibraryVideo> = {},
): AnalysedLibraryVideo {
  return {
    id: "vid-1",
    title: "Common Myths",
    status: "ready",
    kind: "sample",
    contentType: "video/mp4",
    sizeBytes: 0,
    createdAt: "2026-06-10T18:00:00Z",
    updatedAt: "2026-06-10T18:00:00Z",
    analysisStatus: "none",
    analyzedAt: null,
    durationMs: null,
    ...overrides,
  };
}

function track(overrides: Partial<VideoAnalysisTrack> = {}): VideoAnalysisTrack {
  return {
    progressMs: 0,
    analysisError: null,
    frames: null,
    loadFailed: false,
    starting: false,
    startError: null,
    start: vi.fn(),
    retryLoad: vi.fn(),
    ...overrides,
  };
}

describe("AnalysisControl", () => {
  test("an admin can trigger the pre-analysis of a ready, un-analysed video", async () => {
    const t = track();
    render(<AnalysisControl role="admin" video={video()} track={t} />);

    const button = screen.getByRole("button", { name: fr.app.analysis.analyse });
    await userEvent.click(button);
    expect(t.start).toHaveBeenCalledTimes(1);
  });

  test("a non-admin sees no trigger on an un-analysed video", () => {
    render(<AnalysisControl role="guest" video={video()} track={track()} />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  test("no trigger while the video upload is not ready", () => {
    render(
      <AnalysisControl
        role="admin"
        video={video({ status: "pending" })}
        track={track()}
      />,
    );
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  test("the trigger disables and shows the starting label while the request is in flight", () => {
    render(
      <AnalysisControl
        role="admin"
        video={video()}
        track={track({ starting: true })}
      />,
    );
    const button = screen.getByRole("button", { name: fr.app.analysis.starting });
    expect(button).toBeDisabled();
  });

  test("a running analysis shows the polled progress as a chip, for any role", () => {
    render(
      <AnalysisControl
        role="guest"
        video={video({ analysisStatus: "analysing" })}
        track={track({ progressMs: 754_000 })}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      formatTemplate(fr.app.analysis.progress, { position: "12:34" }),
    );
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  test("a completed analysis shows the analysed chip", () => {
    render(
      <AnalysisControl
        role="guest"
        video={video({ analysisStatus: "complete" })}
        track={track({ frames: [] })}
      />,
    );
    expect(screen.getByText(fr.app.analysis.complete)).toBeInTheDocument();
  });

  test("a complete video whose stored result failed to load offers a reload", async () => {
    const t = track({ loadFailed: true });
    render(
      <AnalysisControl
        role="guest"
        video={video({ analysisStatus: "complete" })}
        track={t}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent(
      fr.app.analysis.loadError,
    );
    await userEvent.click(
      screen.getByRole("button", { name: fr.app.analysis.reload }),
    );
    expect(t.retryLoad).toHaveBeenCalledTimes(1);
  });

  test("a failed run surfaces the backend's reason and an admin retry", async () => {
    const t = track({ analysisError: "backend restarted mid-run" });
    render(
      <AnalysisControl
        role="admin"
        video={video({ analysisStatus: "failed" })}
        track={t}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent(
      "backend restarted mid-run",
    );
    await userEvent.click(
      screen.getByRole("button", { name: fr.app.analysis.retry }),
    );
    expect(t.start).toHaveBeenCalledTimes(1);
  });

  test("a failed run shows a status line but no retry to a non-admin", () => {
    render(
      <AnalysisControl
        role="guest"
        video={video({ analysisStatus: "failed" })}
        track={track()}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(fr.app.analysis.failed);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  test.each([
    ["conflict", fr.app.analysis.errors.conflict],
    ["notReady", fr.app.analysis.errors.notReady],
    ["forbidden", fr.app.analysis.errors.forbidden],
    ["failed", fr.app.analysis.errors.failed],
  ] as const)("surfaces a %s trigger failure", (kind, message) => {
    render(
      <AnalysisControl
        role="admin"
        video={video()}
        track={track({ startError: kind })}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(message);
  });

  test("renders nothing without a selected video", () => {
    const { container } = render(
      <AnalysisControl role="admin" video={null} track={track()} />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
