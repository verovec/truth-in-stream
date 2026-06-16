import { describe, expect, test, vi } from "vitest";
import type { LiveAnalysis } from "@/hooks/use-live-analysis";
import { createLiveAnalysisStore } from "./live-analysis-store";
import { emptySummary } from "./summary";

const snapshot = (overrides: Partial<LiveAnalysis> = {}): LiveAnalysis => ({
  statements: [],
  caption: "",
  status: "live",
  summary: emptySummary(),
  claimsFor: () => [],
  ...overrides,
});

describe("createLiveAnalysisStore", () => {
  test("starts with a null snapshot", () => {
    const store = createLiveAnalysisStore();
    expect(store.getSnapshot()).toBeNull();
  });

  test("publishing updates the snapshot and notifies subscribers", () => {
    const store = createLiveAnalysisStore();
    const listener = vi.fn();
    store.subscribe(listener);

    const next = snapshot({ caption: "hello" });
    store.publish(next);

    expect(store.getSnapshot()).toBe(next);
    expect(listener).toHaveBeenCalledTimes(1);
  });

  test("re-publishing the identical snapshot does not notify", () => {
    const store = createLiveAnalysisStore();
    const next = snapshot();
    store.publish(next);

    const listener = vi.fn();
    store.subscribe(listener);
    store.publish(next);

    expect(listener).not.toHaveBeenCalled();
  });

  test("an unsubscribed listener stops receiving notifications", () => {
    const store = createLiveAnalysisStore();
    const listener = vi.fn();
    const unsubscribe = store.subscribe(listener);

    store.publish(snapshot({ caption: "a" }));
    unsubscribe();
    store.publish(snapshot({ caption: "b" }));

    expect(listener).toHaveBeenCalledTimes(1);
  });
});
