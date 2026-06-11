import { useEffect, type Ref, type SyntheticEvent } from "react";

type MediaEventHandler = (event: SyntheticEvent<HTMLVideoElement>) => void;

export type MockReactPlayerProps = {
  ref?: Ref<HTMLVideoElement>;
  src: string;
  volume?: number;
  playbackRate?: number;
  onTimeUpdate?: MediaEventHandler;
  onDurationChange?: MediaEventHandler;
  onWaiting?: MediaEventHandler;
  onCanPlay?: MediaEventHandler;
  onPlaying?: MediaEventHandler;
  onError?: MediaEventHandler;
  onPlay?: MediaEventHandler;
  onPause?: MediaEventHandler;
  onSeeked?: MediaEventHandler;
  onVolumeChange?: MediaEventHandler;
  onRateChange?: MediaEventHandler;
};

export const lastPlayerProps: { current: MockReactPlayerProps | null } = {
  current: null,
};

export default function MockReactPlayer({
  ref,
  src,
  volume,
  playbackRate,
  onTimeUpdate,
  onDurationChange,
  onWaiting,
  onCanPlay,
  onPlaying,
  onError,
  onPlay,
  onPause,
  onSeeked,
  onVolumeChange,
  onRateChange,
}: MockReactPlayerProps) {
  useEffect(() => {
    lastPlayerProps.current = {
      ref,
      src,
      volume,
      playbackRate,
      onTimeUpdate,
      onDurationChange,
      onWaiting,
      onCanPlay,
      onPlaying,
      onError,
      onPlay,
      onPause,
      onSeeked,
      onVolumeChange,
      onRateChange,
    };
  });

  return (
    <video
      data-testid="media"
      ref={ref}
      src={src}
      onTimeUpdate={onTimeUpdate}
      onDurationChange={onDurationChange}
      onWaiting={onWaiting}
      onCanPlay={onCanPlay}
      onPlaying={onPlaying}
      onError={onError}
      onPlay={onPlay}
      onPause={onPause}
      onSeeked={onSeeked}
      onVolumeChange={onVolumeChange}
      onRateChange={onRateChange}
    />
  );
}
