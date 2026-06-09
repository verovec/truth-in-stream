import { useEffect, type Ref, type SyntheticEvent } from "react";

type MediaEventHandler = (event: SyntheticEvent<HTMLVideoElement>) => void;

export type MockReactPlayerProps = {
  ref?: Ref<HTMLVideoElement>;
  src: string;
  volume?: number;
  playbackRate?: number;
  onTimeUpdate?: MediaEventHandler;
  onDurationChange?: MediaEventHandler;
  onPlay?: MediaEventHandler;
  onPause?: MediaEventHandler;
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
  onPlay,
  onPause,
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
      onPlay,
      onPause,
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
      onPlay={onPlay}
      onPause={onPause}
      onVolumeChange={onVolumeChange}
      onRateChange={onRateChange}
    />
  );
}
