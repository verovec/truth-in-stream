import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { seekTarget } from "@/lib/video/thumbnail";
import { VideoPoster } from "./video-poster";

describe("VideoPoster", () => {
  test("shows the gradient monogram when there is no frame", () => {
    const { container } = render(
      <VideoPoster seed="vid-1" title="Common Myths" />,
    );

    expect(screen.getByText("CM")).toBeInTheDocument();
    expect(container.querySelector("video")).toBeNull();
  });

  test("renders the captured frame when a source is provided", () => {
    const { container } = render(
      <VideoPoster seed="vid-1" title="Common Myths" frameSrc="frame-url" />,
    );

    const video = container.querySelector("video");
    expect(video).not.toBeNull();
    expect(video).toHaveAttribute("src", "frame-url");
    expect(video).toHaveAttribute("aria-hidden", "true");
  });

  test("shows a loading spinner while a captured frame is not yet seekable", () => {
    const { container } = render(
      <VideoPoster seed="vid-1" title="Common Myths" frameSrc="frame-url" />,
    );

    expect(container.querySelector(".animate-spin")).not.toBeNull();
  });

  test("replaces the loading spinner with the play overlay once seeked", () => {
    const { container } = render(
      <VideoPoster seed="vid-1" title="Common Myths" frameSrc="frame-url" />,
    );
    const video = container.querySelector("video") as HTMLVideoElement;

    fireEvent.loadedMetadata(video);
    fireEvent.seeked(video);

    expect(container.querySelector(".animate-spin")).toBeNull();
  });

  test("shows no spinner when there is no captured frame", () => {
    const { container } = render(
      <VideoPoster seed="vid-1" title="Common Myths" />,
    );

    expect(container.querySelector(".animate-spin")).toBeNull();
  });

  test("seeks the frame and shows the duration badge once seekable", () => {
    const { container } = render(
      <VideoPoster seed="vid-1" title="Common Myths" frameSrc="frame-url" />,
    );
    const video = container.querySelector("video") as HTMLVideoElement;
    Object.defineProperty(video, "duration", { configurable: true, value: 95 });

    // No badge before metadata/seek have resolved.
    expect(screen.queryByText("1:35")).not.toBeInTheDocument();

    fireEvent.loadedMetadata(video);
    expect(video.currentTime).toBe(seekTarget(95));

    fireEvent.seeked(video);
    expect(screen.getByText("1:35")).toBeInTheDocument();
  });

  test("omits the duration badge when the duration is unknown", () => {
    const { container } = render(
      <VideoPoster seed="vid-1" title="Common Myths" frameSrc="frame-url" />,
    );
    const video = container.querySelector("video") as HTMLVideoElement;
    Object.defineProperty(video, "duration", {
      configurable: true,
      value: Number.NaN,
    });

    fireEvent.loadedMetadata(video);
    fireEvent.seeked(video);

    expect(container.querySelector("video")).not.toBeNull();
    expect(screen.queryByText(/\d+:\d{2}/)).not.toBeInTheDocument();
  });

  test("falls back to the gradient when the frame fails to load", () => {
    const { container } = render(
      <VideoPoster seed="vid-1" title="Common Myths" frameSrc="frame-url" />,
    );
    const video = container.querySelector("video") as HTMLVideoElement;

    fireEvent.error(video);

    expect(container.querySelector("video")).toBeNull();
    expect(screen.getByText("CM")).toBeInTheDocument();
  });

  test("keeps the children overlay slot", () => {
    render(
      <VideoPoster seed="vid-1" title="Common Myths" frameSrc="frame-url">
        <span>badge-slot</span>
      </VideoPoster>,
    );

    expect(screen.getByText("badge-slot")).toBeInTheDocument();
  });
});
