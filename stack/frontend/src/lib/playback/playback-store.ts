export type PlaybackSnapshot = {
  currentTime: number;
  duration: number;
  paused: boolean;
};

export type PlaybackStore = {
  getSnapshot: () => PlaybackSnapshot;
  subscribe: (listener: () => void) => () => void;
  update: (changes: Partial<PlaybackSnapshot>) => void;
  seekTo: (seconds: number) => void;
  registerSeekHandler: (handler: (seconds: number) => void) => () => void;
};

const IDLE_SNAPSHOT: PlaybackSnapshot = {
  currentTime: 0,
  duration: 0,
  paused: true,
};

export function createPlaybackStore(): PlaybackStore {
  let snapshot = IDLE_SNAPSHOT;
  let seekHandler: ((seconds: number) => void) | null = null;
  const listeners = new Set<() => void>();

  return {
    getSnapshot: () => snapshot,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    update: (changes) => {
      const keys = Object.keys(changes) as (keyof PlaybackSnapshot)[];
      if (keys.every((key) => Object.is(snapshot[key], changes[key]))) {
        return;
      }
      snapshot = { ...snapshot, ...changes };
      for (const listener of listeners) {
        listener();
      }
    },
    seekTo: (seconds) => {
      seekHandler?.(seconds);
    },
    registerSeekHandler: (handler) => {
      seekHandler = handler;
      return () => {
        if (seekHandler === handler) {
          seekHandler = null;
        }
      };
    },
  };
}
