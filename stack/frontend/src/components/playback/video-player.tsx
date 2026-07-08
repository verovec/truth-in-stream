"use client";

import { useEffect, useRef, useState, type SyntheticEvent } from "react";
import ReactPlayer from "react-player";
import { LoadingSpinner } from "@/components/loading-spinner";
import { useAppI18n } from "@/components/i18n/app-i18n";
import {
  applyPlaybackCommand,
  resolvePlaybackCommand,
} from "@/lib/playback/keyboard";
import { usePlaybackStore } from "./playback-provider";

type VideoPlayerProps = {
  src: string;
  title: string;
};

// PlayState drives the loading overlay: a clip starts "buffering" (nothing
// decoded yet), reaches "playing" once it can play through, returns to
// "buffering" while it stalls to refill, and lands on "error" if the media
// element gives up. The overlay shows a spinner while buffering and a message on
// error, so a slow or broken source never reads as a silently blank black box.
type PlayState = "buffering" | "playing" | "error";

export function VideoPlayer({ src, title }: VideoPlayerProps) {
  const store = usePlaybackStore();
  const containerRef = useRef<HTMLElement>(null);
  const mediaRef = useRef<HTMLVideoElement>(null);
  const [volume, setVolume] = useState(1);
  const [playbackRate, setPlaybackRate] = useState(1);
  const [playState, setPlayState] = useState<PlayState>("buffering");

  // A new source starts buffering from scratch; without this, switching clips
  // would keep the previous clip's "playing" state and hide the spinner. The
  // reset happens during render when src changes (React's sanctioned
  // adjust-state-on-prop-change pattern) rather than in an effect, which would
  // cascade an extra render.
  const [prevSrc, setPrevSrc] = useState(src);
  if (src !== prevSrc) {
    setPrevSrc(src);
    setPlayState("buffering");
  }

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

  // onWaiting can fire after a fatal error as the element tears down; an error is
  // terminal for this source, so it is never overwritten back to buffering.
  const markBuffering = () =>
    setPlayState((prev) => (prev === "error" ? prev : "buffering"));
  const markPlaying = () =>
    setPlayState((prev) => (prev === "error" ? prev : "playing"));

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
      aria-busy={playState === "buffering"}
      className="relative overflow-hidden rounded-2xl border border-black/10 bg-night shadow-lg shadow-bleu/5 dark:border-white/10 dark:shadow-black/40"
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
        onWaiting={markBuffering}
        onCanPlay={markPlaying}
        onPlaying={markPlaying}
        onError={() => setPlayState("error")}
        onPlay={() => store.update({ paused: false })}
        onPause={() => store.update({ paused: true })}
        onSeeked={() => store.notifySeeked()}
        onVolumeChange={(event) => setVolume(event.currentTarget.volume)}
        onRateChange={(event) =>
          setPlaybackRate(event.currentTarget.playbackRate)
        }
      />
      {/* Buffering shows a spinner over the video while it has nothing to play
          yet; the spinner is pointer-events-none so the native controls stay
          clickable once they appear. */}
      {playState === "buffering" ? <LoadingSpinner size="lg" /> : null}
      {playState === "error" ? <PlayerErrorOverlay /> : null}
    </section>
  );
}

// PlayerErrorOverlay replaces a silently blank player when the media element
// cannot load the source, so the operator sees a cause rather than a black box.
function PlayerErrorOverlay() {
  const { t } = useAppI18n();
  return (
    <div
      role="alert"
      className="absolute inset-0 flex flex-col items-center justify-center gap-1 bg-night/85 p-4 text-center text-sm text-paper"
    >
      <p className="font-medium">{t.player.playError}</p>
      <p className="text-xs text-paper/60">{t.player.playErrorHint}</p>
    </div>
  );
}
