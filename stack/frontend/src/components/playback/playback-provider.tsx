"use client";

import {
  createContext,
  useContext,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import {
  createPlaybackStore,
  type PlaybackSnapshot,
  type PlaybackStore,
} from "@/lib/playback/playback-store";

const PlaybackContext = createContext<PlaybackStore | null>(null);

export function PlaybackProvider({ children }: { children: ReactNode }) {
  const [store] = useState(createPlaybackStore);
  return (
    <PlaybackContext.Provider value={store}>
      {children}
    </PlaybackContext.Provider>
  );
}

export function usePlaybackStore(): PlaybackStore {
  const store = useContext(PlaybackContext);
  if (!store) {
    throw new Error("usePlaybackStore requires a PlaybackProvider ancestor");
  }
  return store;
}

export function usePlayback(): PlaybackSnapshot;
export function usePlayback<Selected>(
  selector: (snapshot: PlaybackSnapshot) => Selected,
): Selected;
export function usePlayback<Selected>(
  selector?: (snapshot: PlaybackSnapshot) => Selected,
): PlaybackSnapshot | Selected {
  const store = usePlaybackStore();
  const getSelection: () => PlaybackSnapshot | Selected = selector
    ? () => selector(store.getSnapshot())
    : store.getSnapshot;
  return useSyncExternalStore(store.subscribe, getSelection, getSelection);
}
