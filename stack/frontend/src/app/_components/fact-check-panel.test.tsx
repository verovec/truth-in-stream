import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import {
  json,
  resultsRoute,
  statusRoute,
  stubBackend,
  submitRoute,
} from "@/test/fact-check";
import { renderWithPlayback } from "@/test/playback";
import { FactCheckPanel } from "./fact-check-panel";

const RESULTS = {
  video_id: "v1",
  segments: [
    {
      start: 0,
      end: 4.5,
      text: "The earth orbits the sun.",
      matches: [
        {
          claim: "Earth completes one orbit of the Sun per year",
          verdict: "corroborates",
          sources: [{ title: "NASA", url: "https://nasa.example/orbit" }],
          similarity: 0.93,
        },
      ],
    },
    {
      start: 4.5,
      end: 9,
      text: "We never landed on the moon.",
      matches: [
        {
          claim: "Apollo 11 landed humans on the Moon in 1969",
          verdict: "contradicts",
          sources: [
            { title: "NASA Archive", url: "https://nasa.example/apollo" },
            { title: "ESA", url: "https://esa.example/apollo" },
          ],
          similarity: 0.88,
        },
        {
          claim: "Lunar landing footage is unedited",
          verdict: "unclear",
          sources: [{ title: "Fact DB", url: "https://factdb.example/footage" }],
          similarity: 0.61,
        },
      ],
    },
    {
      start: 12,
      end: 15,
      text: "Anyway, how is the weather?",
      matches: [],
    },
  ],
};

function stubCompletedVideo() {
  stubBackend([
    submitRoute(json(200, { video_id: "v1", status: "complete" })),
    resultsRoute(json(200, RESULTS)),
  ]);
}

function renderPanel() {
  return renderWithPlayback(
    <FactCheckPanel source="https://example.com/v.mp4" pollIntervalMs={0} />,
  );
}

async function renderReadyPanel() {
  stubCompletedVideo();
  const rendered = renderPanel();
  await screen.findByRole("list");
  return rendered;
}

describe("FactCheckPanel", () => {
  test("renders as a complementary landmark with a heading", async () => {
    stubCompletedVideo();
    renderPanel();

    expect(
      screen.getByRole("complementary", { name: /fact checks/i }),
    ).toBeInTheDocument();
    await screen.findByRole("list");
  });

  test("shows the playback position as it advances", async () => {
    stubCompletedVideo();
    const { store } = renderPanel();

    expect(screen.getByText("0:00")).toBeInTheDocument();

    act(() => store.update({ currentTime: 754 }));

    expect(screen.getByText("12:34")).toBeInTheDocument();
    await screen.findByRole("list");
  });

  test("shows a loading state while the video is being submitted", () => {
    let releaseSubmit!: () => void;
    const submitGate = new Promise<void>((resolve) => {
      releaseSubmit = resolve;
    });
    stubBackend([
      submitRoute(() =>
        submitGate.then(json(200, { video_id: "v1", status: "complete" })),
      ),
      resultsRoute(json(200, RESULTS)),
    ]);

    renderPanel();

    expect(screen.getByText(/preparing fact checks/i)).toBeInTheDocument();
    releaseSubmit();
  });

  test("shows processing progress while the pipeline runs", async () => {
    stubBackend([
      submitRoute(json(202, { video_id: "v1", status: "processing" })),
      statusRoute(
        json(200, {
          video_id: "v1",
          status: "processing",
          segments_total: 10,
          segments_done: 3,
        }),
        () => new Promise<Response>(() => undefined),
      ),
    ]);

    renderPanel();

    const progressbar = await screen.findByRole("progressbar");
    expect(progressbar).toHaveAttribute("aria-valuenow", "3");
    expect(progressbar).toHaveAttribute("aria-valuemax", "10");
    expect(screen.getByText(/3 of 10 segments checked/i)).toBeInTheDocument();
  });

  test("shows an error state when processing fails", async () => {
    stubBackend([
      submitRoute(json(202, { video_id: "v1", status: "processing" })),
      statusRoute(
        json(200, {
          video_id: "v1",
          status: "failed",
          segments_total: 10,
          segments_done: 4,
          error: "transcription unavailable",
        }),
      ),
    ]);

    renderPanel();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("transcription unavailable");
  });

  test("retries from the error state and recovers to results", async () => {
    const user = userEvent.setup();
    stubBackend([
      submitRoute(
        json(503, { error: "processing queue is full, retry later" }),
        json(200, { video_id: "v1", status: "complete" }),
      ),
      resultsRoute(json(200, RESULTS)),
    ]);

    renderPanel();

    const retry = await screen.findByRole("button", { name: /try again/i });
    await user.click(retry);

    await screen.findByRole("list");
  });

  test("shows an empty state when the video has no segments", async () => {
    stubBackend([
      submitRoute(json(200, { video_id: "v1", status: "complete" })),
      resultsRoute(json(200, { video_id: "v1", segments: [] })),
    ]);

    renderPanel();

    expect(
      await screen.findByText(/no speech segments were found/i),
    ).toBeInTheDocument();
  });

  test("renders claim, verdict, and source links for each match", async () => {
    await renderReadyPanel();

    expect(
      screen.getByText("Earth completes one orbit of the Sun per year"),
    ).toBeInTheDocument();
    expect(screen.getByText(/^corroborates$/i)).toBeInTheDocument();
    expect(screen.getByText(/^contradicts$/i)).toBeInTheDocument();
    expect(screen.getByText(/^unclear$/i)).toBeInTheDocument();

    const nasaLink = screen.getByRole("link", { name: "NASA" });
    expect(nasaLink).toHaveAttribute("href", "https://nasa.example/orbit");
    expect(screen.getByRole("link", { name: "ESA" })).toHaveAttribute(
      "href",
      "https://esa.example/apollo",
    );
  });

  test("shows a neutral state for segments without a confident match", async () => {
    await renderReadyPanel();

    const items = screen.getAllByRole("listitem");
    expect(items[2]).toHaveTextContent(/no confident match/i);
    expect(items[2]).not.toHaveTextContent(/error/i);
  });

  test("highlights the segment containing the playback position", async () => {
    const { store } = await renderReadyPanel();

    const items = screen.getAllByRole("listitem");
    expect(items[0]).toHaveAttribute("aria-current", "true");
    expect(items[1]).not.toHaveAttribute("aria-current");

    act(() => store.update({ currentTime: 5 }));
    expect(items[0]).not.toHaveAttribute("aria-current");
    expect(items[1]).toHaveAttribute("aria-current", "true");
  });

  test("highlights nothing in a gap between segments", async () => {
    const { store } = await renderReadyPanel();

    act(() => store.update({ currentTime: 10.5 }));

    for (const item of screen.getAllByRole("listitem")) {
      expect(item).not.toHaveAttribute("aria-current");
    }
  });

  test("stays in sync when scrubbing back and forth", async () => {
    const { store } = await renderReadyPanel();
    const items = screen.getAllByRole("listitem");

    act(() => store.update({ currentTime: 13 }));
    expect(items[2]).toHaveAttribute("aria-current", "true");

    act(() => store.update({ currentTime: 1 }));
    expect(items[0]).toHaveAttribute("aria-current", "true");
    expect(items[2]).not.toHaveAttribute("aria-current");

    act(() => store.update({ currentTime: 99 }));
    for (const item of screen.getAllByRole("listitem")) {
      expect(item).not.toHaveAttribute("aria-current");
    }
  });

  test("clicking a result seeks to the segment start", async () => {
    const user = userEvent.setup();
    const { store } = await renderReadyPanel();
    const seekHandler = vi.fn();
    store.registerSeekHandler(seekHandler);

    await user.click(
      screen.getByRole("button", { name: /we never landed on the moon/i }),
    );

    expect(seekHandler).toHaveBeenCalledWith(4.5);
  });

  test("source links do not trigger a seek", async () => {
    const user = userEvent.setup();
    const { store } = await renderReadyPanel();
    const seekHandler = vi.fn();
    store.registerSeekHandler(seekHandler);

    await user.click(screen.getByRole("link", { name: "NASA" }));

    expect(seekHandler).not.toHaveBeenCalled();
  });

  test("recovers when results briefly race their completion marker", async () => {
    stubBackend([
      submitRoute(json(200, { video_id: "v1", status: "complete" })),
      statusRoute(
        json(200, {
          video_id: "v1",
          status: "complete",
          segments_total: 3,
          segments_done: 3,
        }),
      ),
      resultsRoute(
        json(409, { error: "processing has not completed" }),
        json(200, RESULTS),
      ),
    ]);

    renderPanel();

    await waitFor(() =>
      expect(screen.getByRole("list")).toBeInTheDocument(),
    );
  });
});
