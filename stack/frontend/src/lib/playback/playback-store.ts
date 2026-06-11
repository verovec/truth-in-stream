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
  // The live audio path captures from the same media element the player owns;
  // the player registers it here so the live hook can attach without prop
  // drilling the element through the tree.
  registerMediaElement: (element: HTMLMediaElement) => () => void;
  getMediaElement: () => HTMLMediaElement | null;
  // Seek events drive the live session to reset cleanly. The player forwards the
  // media element's "seeked" event (fired for both scrubbing and programmatic
  // seeks) here; the live hook subscribes.
  notifySeeked: () => void;
  subscribeSeeked: (listener: () => void) => () => void;
};

const IDLE_SNAPSHOT: PlaybackSnapshot = {
  currentTime: 0,
  duration: 0,
  paused: true,
};

export function createPlaybackStore(): PlaybackStore {
  let snapshot = IDLE_SNAPSHOT;
  let seekHandler: ((seconds: number) => void) | null = null;
  let mediaElement: HTMLMediaElement | null = null;
  const listeners = new Set<() => void>();
  const seekListeners = new Set<() => void>();

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
    registerMediaElement: (element) => {
      mediaElement = element;
      return () => {
        if (mediaElement === element) {
          mediaElement = null;
        }
      };
    },
    getMediaElement: () => mediaElement,
    notifySeeked: () => {
      for (const listener of seekListeners) {
        listener();
      }
    },
    subscribeSeeked: (listener) => {
      seekListeners.add(listener);
      return () => seekListeners.delete(listener);
    },
  };
}
