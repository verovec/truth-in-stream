import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import {
  PlaybackProvider,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import type { PlaybackStore } from "@/lib/playback/playback-store";

export function renderWithPlayback(ui: ReactNode): { store: PlaybackStore } {
  let store: PlaybackStore | undefined;
  function StoreProbe() {
    store = usePlaybackStore();
    return null;
  }
  render(
    <PlaybackProvider>
      <StoreProbe />
      {ui}
    </PlaybackProvider>,
  );
  if (!store) {
    throw new Error("PlaybackProvider did not mount");
  }
  return { store };
}
