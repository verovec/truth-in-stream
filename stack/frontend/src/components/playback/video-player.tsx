"use client";

import { useEffect, useRef, useState, type SyntheticEvent } from "react";
import ReactPlayer from "react-player";
import {
  applyPlaybackCommand,
  resolvePlaybackCommand,
} from "@/lib/playback/keyboard";
import { usePlaybackStore } from "./playback-provider";

type VideoPlayerProps = {
  src: string;
  title: string;
};

export function VideoPlayer({ src, title }: VideoPlayerProps) {
  const store = usePlaybackStore();
  const containerRef = useRef<HTMLElement>(null);
  const mediaRef = useRef<HTMLVideoElement>(null);
  const [volume, setVolume] = useState(1);
  const [playbackRate, setPlaybackRate] = useState(1);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const command = resolvePlaybackCommand(event);
      if (!command) {
        return;
      }
      event.preventDefault();
      if (command.kind === "toggle-fullscreen") {
        if (document.fullscreenElement) {
          void document.exitFullscreen();
        } else {
          void containerRef.current?.requestFullscreen();
        }
        return;
      }
      const media = mediaRef.current;
      if (media) {
        applyPlaybackCommand(media, command);
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  useEffect(() => {
    return store.registerSeekHandler((seconds) => {
      const media = mediaRef.current;
      if (media) {
        media.currentTime = Math.max(seconds, 0);
      }
    });
  }, [store]);

  // Expose the media element to the live audio path so it can capture the
  // playing audio. crossOrigin is set as a prop (below) rather than here, so it
  // lands with the src and is honoured before the cross-origin source loads.
  useEffect(() => {
    const media = mediaRef.current;
    if (!media) {
      return;
    }
    return store.registerMediaElement(media);
  }, [store, src]);

  const publishTime = (event: SyntheticEvent<HTMLVideoElement>) => {
    store.update({ currentTime: event.currentTarget.currentTime });
  };
  const publishDuration = (event: SyntheticEvent<HTMLVideoElement>) => {
    const { duration } = event.currentTarget;
    if (Number.isFinite(duration)) {
      store.update({ duration });
    }
  };

  return (
    <section
      ref={containerRef}
      aria-label={title}
      className="overflow-hidden rounded-xl bg-black"
    >
      <ReactPlayer
        ref={mediaRef}
        src={src}
        controls
        playsInline
        crossOrigin="anonymous"
        volume={volume}
        playbackRate={playbackRate}
        style={{ width: "100%", height: "100%", aspectRatio: "16 / 9" }}
        onTimeUpdate={publishTime}
        onDurationChange={publishDuration}
        onPlay={() => store.update({ paused: false })}
        onPause={() => store.update({ paused: true })}
        onSeeked={() => store.notifySeeked()}
        onVolumeChange={(event) => setVolume(event.currentTarget.volume)}
        onRateChange={(event) =>
          setPlaybackRate(event.currentTarget.playbackRate)
        }
      />
    </section>
  );
}
