import { act, fireEvent, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { renderWithPlayback } from "@/test/playback";
import { lastPlayerProps } from "@/test/react-player-mock";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { VideoPlayer } from "./video-player";

vi.mock("react-player", () => import("@/test/react-player-mock"));

function renderPlayer() {
  const { store } = renderWithPlayback(
    <VideoPlayer src="/sample.mp4" title="Sample video" />,
  );
  return { store, media: screen.getByTestId<HTMLVideoElement>("media") };
}

function defineMediaProperty(
  media: HTMLVideoElement,
  property: "currentTime" | "duration",
  value: number,
) {
  Object.defineProperty(media, property, {
    value,
    configurable: true,
    writable: true,
  });
}

describe("VideoPlayer", () => {
  test("renders a labelled player region with the media source", () => {
    const { media } = renderPlayer();

    expect(
      screen.getByRole("region", { name: "Sample video" }),
    ).toBeInTheDocument();
    expect(media).toHaveAttribute("src", "/sample.mp4");
  });

  test("publishes time, duration, and pause state to the playback store", () => {
    const { store, media } = renderPlayer();

    defineMediaProperty(media, "currentTime", 42);
    defineMediaProperty(media, "duration", 300);
    fireEvent.timeUpdate(media);
    fireEvent.durationChange(media);
    fireEvent.play(media);

    expect(store.getSnapshot()).toEqual({
      currentTime: 42,
      duration: 300,
      paused: false,
    });

    fireEvent.pause(media);

    expect(store.getSnapshot().paused).toBe(true);
  });

  test("keeps non-finite durations out of the store", () => {
    const { store, media } = renderPlayer();

    defineMediaProperty(media, "duration", Number.NaN);
    fireEvent.durationChange(media);

    expect(store.getSnapshot().duration).toBe(0);
  });

  test("seeks the media element when the store requests it", () => {
    const { store, media } = renderPlayer();
    defineMediaProperty(media, "currentTime", 0);

    act(() => store.seekTo(120));

    expect(media.currentTime).toBe(120);
  });

  test("mirrors media volume and rate into player props so re-renders cannot reset them", () => {
    const { media } = renderPlayer();

    act(() => {
      media.volume = 0.4;
      fireEvent.volumeChange(media);
    });

    expect(lastPlayerProps.current?.volume).toBe(0.4);

    act(() => {
      media.playbackRate = 1.5;
      fireEvent.rateChange(media);
    });

    expect(lastPlayerProps.current?.playbackRate).toBe(1.5);
  });

  test("toggles play and pause from the keyboard", () => {
    const { media } = renderPlayer();
    const play = vi
      .spyOn(media, "play")
      .mockImplementation(() => Promise.resolve());

    fireEvent.keyDown(document.body, { key: "k" });

    expect(play).toHaveBeenCalledOnce();
  });

  test("seeks and changes volume from the keyboard", () => {
    const { media } = renderPlayer();
    defineMediaProperty(media, "currentTime", 60);
    defineMediaProperty(media, "duration", 300);
    media.volume = 0.5;

    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(media.currentTime).toBe(65);

    fireEvent.keyDown(document.body, { key: "ArrowDown" });
    expect(media.volume).toBe(0.4);

    fireEvent.keyDown(document.body, { key: "m" });
    expect(media.muted).toBe(true);
  });

  test("requests fullscreen on the player region with the f key", () => {
    renderPlayer();
    const region = screen.getByRole("region", { name: "Sample video" });
    const requestFullscreen = vi.fn();
    Object.defineProperty(region, "requestFullscreen", {
      value: requestFullscreen,
      configurable: true,
    });

    fireEvent.keyDown(document.body, { key: "f" });

    expect(requestFullscreen).toHaveBeenCalledOnce();
  });

  test("ignores shortcuts while typing in a form field", () => {
    const { media } = renderPlayer();
    const play = vi
      .spyOn(media, "play")
      .mockImplementation(() => Promise.resolve());
    const input = document.createElement("input");
    document.body.appendChild(input);

    fireEvent.keyDown(input, { key: "k" });

    expect(play).not.toHaveBeenCalled();
    input.remove();
  });

  test("shows a buffering overlay until the media can play", () => {
    const { media } = renderPlayer();
    const region = screen.getByRole("region", { name: "Sample video" });

    expect(region).toHaveAttribute("aria-busy", "true");

    fireEvent.canPlay(media);

    expect(region).toHaveAttribute("aria-busy", "false");
  });

  test("re-shows the buffering overlay when playback stalls", () => {
    const { media } = renderPlayer();
    const region = screen.getByRole("region", { name: "Sample video" });

    fireEvent.playing(media);
    expect(region).toHaveAttribute("aria-busy", "false");

    fireEvent.waiting(media);
    expect(region).toHaveAttribute("aria-busy", "true");
  });

  test("surfaces a media error instead of a blank player", () => {
    const { media } = renderPlayer();
    const region = screen.getByRole("region", { name: "Sample video" });

    fireEvent.error(media);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent(fr.app.player.playError);
    expect(alert).toHaveTextContent(fr.app.player.playErrorHint);
    expect(region).toHaveAttribute("aria-busy", "false");

    // An error is terminal for the source: a late stall event must not revert it
    // to a buffering spinner that would hide the cause.
    fireEvent.waiting(media);
    expect(region).toHaveAttribute("aria-busy", "false");
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });
});
