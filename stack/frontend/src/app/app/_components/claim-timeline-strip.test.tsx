import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { describe, expect, test, vi } from "vitest";
import { LiveAnalysisContext } from "@/components/live/live-analysis-provider";
import {
  PlaybackProvider,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import type { LiveFrame } from "@/lib/live/frames";
import { hydrateAnalysis } from "@/lib/live/hydrate";
import {
  createLiveAnalysisStore,
  type LiveAnalysisSnapshot,
} from "@/lib/live/live-analysis-store";
import { ClaimTimelineStrip } from "./claim-timeline-strip";

// PlaybackRig stands in for the player: it publishes the video duration and
// owns the seek handler the strip's clicks land on.
function PlaybackRig({
  duration,
  onSeek,
}: {
  duration: number;
  onSeek: (seconds: number) => void;
}) {
  const store = usePlaybackStore();
  useEffect(() => {
    store.update({ duration });
  }, [store, duration]);
  useEffect(() => store.registerSeekHandler(onSeek), [store, onSeek]);
  return null;
}

function renderStrip({
  snapshot,
  duration = 100,
  onSeek = vi.fn(),
}: {
  snapshot: LiveAnalysisSnapshot;
  duration?: number;
  onSeek?: (seconds: number) => void;
}) {
  const store = createLiveAnalysisStore();
  store.publish(snapshot);
  return render(
    <PlaybackProvider>
      <PlaybackRig duration={duration} onSeek={onSeek} />
      <LiveAnalysisContext.Provider value={store}>
        <ClaimTimelineStrip />
      </LiveAnalysisContext.Provider>
    </PlaybackProvider>,
  );
}

// A stored two-statement analysis: three verdicts (one per color) and one claim
// still pending, which must leave no mark.
const ANALYSED_FRAMES: LiveFrame[] = [
  {
    type: "subtitle",
    id: "s1",
    start: 10,
    end: 20,
    text: "first statement",
    speaker: "A",
  },
  {
    type: "subtitle",
    id: "s2",
    start: 40,
    end: 50,
    text: "second statement",
    speaker: "B",
  },
  {
    type: "claims",
    id: "s1",
    claims: [
      { claimId: "c1", text: "the credible claim", status: "pending" },
      { claimId: "c2", text: "the disputed claim", status: "pending" },
    ],
  },
  {
    type: "claims",
    id: "s2",
    claims: [
      { claimId: "c3", text: "the unverifiable claim", status: "pending" },
      { claimId: "c4", text: "the pending claim", status: "pending" },
    ],
  },
  {
    type: "claim_result",
    id: "s1",
    claimId: "c1",
    status: "verified",
    source: "verified",
    verdict: "credible",
  },
  {
    type: "claim_result",
    id: "s1",
    claimId: "c2",
    status: "verified",
    source: "verified",
    verdict: "disputed",
  },
  {
    type: "claim_result",
    id: "s2",
    claimId: "c3",
    status: "verified",
    source: "verified",
    verdict: "unverifiable",
  },
];

const markerName = (text: string, verdict: string) =>
  formatTemplate(fr.app.analysis.timeline.marker, { text, verdict });

describe("ClaimTimelineStrip", () => {
  test("marks every checked claim colored by verdict and skips pending ones", () => {
    renderStrip({ snapshot: hydrateAnalysis(ANALYSED_FRAMES) });

    const strip = screen.getByRole("region", {
      name: fr.app.analysis.timeline.ariaLabel,
    });
    expect(strip).toBeInTheDocument();

    const credible = screen.getByRole("button", {
      name: markerName("the credible claim", fr.app.claims.verdicts.credible),
    });
    const disputed = screen.getByRole("button", {
      name: markerName("the disputed claim", fr.app.claims.verdicts.disputed),
    });
    const unverifiable = screen.getByRole("button", {
      name: markerName(
        "the unverifiable claim",
        fr.app.claims.verdicts.unverifiable,
      ),
    });
    expect(credible.className).toContain("bg-verdict-credible");
    expect(disputed.className).toContain("bg-verdict-disputed");
    expect(unverifiable.className).toContain("bg-verdict-unverifiable");

    // The pending claim leaves no mark and no fourth button exists.
    expect(
      screen.queryByRole("button", { name: /the pending claim/ }),
    ).not.toBeInTheDocument();
    expect(screen.getAllByRole("button")).toHaveLength(3);
  });

  test("positions markers by their statement span against the duration", () => {
    renderStrip({ snapshot: hydrateAnalysis(ANALYSED_FRAMES) });

    const credible = screen.getByRole("button", {
      name: markerName("the credible claim", fr.app.claims.verdicts.credible),
    });
    expect(credible.style.left).toBe("10%");
    expect(credible.style.width).toBe("10%");
    const unverifiable = screen.getByRole("button", {
      name: markerName(
        "the unverifiable claim",
        fr.app.claims.verdicts.unverifiable,
      ),
    });
    expect(unverifiable.style.left).toBe("40%");
  });

  test("clicking a marker seeks playback to the claim's moment", async () => {
    const onSeek = vi.fn();
    const user = userEvent.setup();
    renderStrip({ snapshot: hydrateAnalysis(ANALYSED_FRAMES), onSeek });

    await user.click(
      screen.getByRole("button", {
        name: markerName(
          "the unverifiable claim",
          fr.app.claims.verdicts.unverifiable,
        ),
      }),
    );
    expect(onSeek).toHaveBeenCalledWith(40);
  });

  test("markers are keyboard reachable and activate the seek", async () => {
    const onSeek = vi.fn();
    const user = userEvent.setup();
    renderStrip({ snapshot: hydrateAnalysis(ANALYSED_FRAMES), onSeek });

    await user.tab();
    expect(
      screen.getByRole("button", {
        name: markerName("the credible claim", fr.app.claims.verdicts.credible),
      }),
    ).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(onSeek).toHaveBeenCalledWith(10);
  });

  test("the tooltip identifies the claim text and its verdict", () => {
    renderStrip({ snapshot: hydrateAnalysis(ANALYSED_FRAMES) });

    const disputed = screen.getByRole("button", {
      name: markerName("the disputed claim", fr.app.claims.verdicts.disputed),
    });
    expect(disputed).toHaveTextContent("the disputed claim");
    expect(disputed).toHaveTextContent(fr.app.claims.verdicts.disputed);
  });

  test("overlapping markers stack into lanes and stay individually interactive", async () => {
    const onSeek = vi.fn();
    const user = userEvent.setup();
    renderStrip({ snapshot: hydrateAnalysis(ANALYSED_FRAMES), onSeek });

    // c1 and c2 share the same statement span [10, 20]: same left, different
    // lanes, so neither covers the other.
    const credible = screen.getByRole("button", {
      name: markerName("the credible claim", fr.app.claims.verdicts.credible),
    });
    const disputed = screen.getByRole("button", {
      name: markerName("the disputed claim", fr.app.claims.verdicts.disputed),
    });
    expect(credible.style.left).toBe(disputed.style.left);
    expect(credible.style.top).not.toBe(disputed.style.top);

    await user.click(credible);
    await user.click(disputed);
    expect(onSeek).toHaveBeenCalledTimes(2);
  });

  test("renders nothing when the store holds no analysis", () => {
    const { container } = renderStrip({ snapshot: null });
    expect(container).toBeEmptyDOMElement();
  });

  test("renders nothing when the analysis produced no checked claims", () => {
    const noVerdicts = ANALYSED_FRAMES.filter(
      (frame) => frame.type !== "claim_result",
    );
    const { container } = renderStrip({
      snapshot: hydrateAnalysis(noVerdicts),
    });
    expect(container).toBeEmptyDOMElement();
  });

  test("renders nothing before the video duration is known", () => {
    const { container } = renderStrip({
      snapshot: hydrateAnalysis(ANALYSED_FRAMES),
      duration: 0,
    });
    expect(container).toBeEmptyDOMElement();
  });
});
