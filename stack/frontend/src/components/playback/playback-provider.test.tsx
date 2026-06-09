import { act, render, renderHook, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, test, vi } from "vitest";
import {
  PlaybackProvider,
  usePlayback,
  usePlaybackStore,
} from "./playback-provider";

const wrapper = ({ children }: { children: ReactNode }) => (
  <PlaybackProvider>{children}</PlaybackProvider>
);

describe("usePlaybackStore", () => {
  test("throws outside a PlaybackProvider", () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);

    expect(() => renderHook(() => usePlaybackStore())).toThrow(
      /PlaybackProvider/,
    );
  });

  test("returns a stable store across re-renders", () => {
    const { result, rerender } = renderHook(() => usePlaybackStore(), {
      wrapper,
    });
    const first = result.current;

    rerender();

    expect(result.current).toBe(first);
  });
});

describe("usePlayback", () => {
  test("with a selector, re-renders only when the selected value changes", () => {
    let renders = 0;
    function WholeSeconds() {
      const seconds = usePlayback((snapshot) =>
        Math.floor(snapshot.currentTime),
      );
      renders += 1;
      return <span data-testid="seconds">{seconds}</span>;
    }
    let store: ReturnType<typeof usePlaybackStore> | undefined;
    function Player() {
      store = usePlaybackStore();
      return null;
    }
    render(
      <PlaybackProvider>
        <Player />
        <WholeSeconds />
      </PlaybackProvider>,
    );
    const rendersAfterMount = renders;

    act(() => store?.update({ currentTime: 1.2 }));
    act(() => store?.update({ currentTime: 1.7 }));

    expect(screen.getByTestId("seconds").textContent).toBe("1");
    expect(renders).toBe(rendersAfterMount + 1);

    act(() => store?.update({ currentTime: 2.1 }));

    expect(screen.getByTestId("seconds").textContent).toBe("2");
  });

  test("reflects store updates as playback advances", () => {
    function CurrentTime() {
      const { currentTime } = usePlayback();
      return <span data-testid="time">{currentTime}</span>;
    }
    let store: ReturnType<typeof usePlaybackStore> | undefined;
    function Player() {
      store = usePlaybackStore();
      return null;
    }
    render(
      <PlaybackProvider>
        <Player />
        <CurrentTime />
      </PlaybackProvider>,
    );

    expect(screen.getByTestId("time").textContent).toBe("0");

    act(() => store?.update({ currentTime: 42 }));

    expect(screen.getByTestId("time").textContent).toBe("42");
  });
});
