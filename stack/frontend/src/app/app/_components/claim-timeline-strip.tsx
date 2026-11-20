"use client";

import { useMemo } from "react";
import { useLiveAnalysisSelector } from "@/components/live/live-analysis-provider";
import {
  usePlayback,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import type { ClaimVerdict } from "@/lib/live/frames";
import {
  laneCount,
  timelineMarkers,
  type TimelineMarker,
} from "@/lib/live/timeline";

// The strip reuses the shared semantic verdict tokens: credible and disputed
// read at full strength, unverifiable is deliberately muted so the strip's
// "hot moments" are the confirmed and contested claims.
const MARKER_STYLES: Record<ClaimVerdict, string> = {
  credible: "bg-verdict-credible",
  disputed: "bg-verdict-disputed",
  unverifiable: "bg-verdict-unverifiable/40 dark:bg-verdict-unverifiable/30",
};

// Lane geometry in rem: marker height plus the stride between stacked lanes.
// Overlapping markers stack downward, so a dense cluster grows the strip
// instead of hiding markers under each other.
const MARKER_HEIGHT_REM = 0.5;
const LANE_STRIDE_REM = 0.625;

// ClaimTimelineStrip marks every checked claim of a pre-analysed video on a bar
// aligned to the video duration, directly under the player's native controls
// (never a rebuilt scrubber). It mounts only on the analysed playback path -
// the wiring point gates it on the hydrated stored analysis - and renders
// nothing while the snapshot or the duration has not arrived, or when the
// analysis produced no checked claims, so a video without verdicts shows no
// empty shell.
export function ClaimTimelineStrip() {
  const statements = useLiveAnalysisSelector(
    (snapshot) => snapshot?.statements ?? null,
  );
  const claimsFor = useLiveAnalysisSelector(
    (snapshot) => snapshot?.claimsFor ?? null,
  );
  const duration = usePlayback((snapshot) => snapshot.duration);
  const { t } = useAppI18n();

  const markers = useMemo(
    () =>
      statements !== null && claimsFor !== null
        ? timelineMarkers(statements, claimsFor, duration)
        : [],
    [statements, claimsFor, duration],
  );

  if (markers.length === 0) {
    return null;
  }
  const lanes = laneCount(markers);
  return (
    <section
      aria-label={t.analysis.timeline.ariaLabel}
      className="w-full rounded-md bg-black/5 px-1 py-1 dark:bg-white/5"
    >
      <div
        className="relative w-full"
        style={{
          height: `${(lanes - 1) * LANE_STRIDE_REM + MARKER_HEIGHT_REM}rem`,
        }}
      >
        {markers.map((marker) => (
          <MarkerButton
            key={`${marker.statementId}:${marker.claimId}`}
            marker={marker}
          />
        ))}
      </div>
    </section>
  );
}

// MarkerButton is one checked claim on the strip: a real button (keyboard
// reachable, named with the claim text and verdict) that seeks playback to the
// claim's moment. The tooltip repeats the name visually on hover and keyboard
// focus; it ignores the pointer so it never steals a neighbouring marker's
// hover in a dense cluster.
function MarkerButton({ marker }: { marker: TimelineMarker }) {
  const store = usePlaybackStore();
  const { t } = useAppI18n();
  const label = formatTemplate(t.analysis.timeline.marker, {
    text: marker.text,
    verdict: t.claims.verdicts[marker.verdict],
  });
  return (
    <button
      type="button"
      aria-label={label}
      onClick={() => store.seekTo(marker.seekTo)}
      className={`group absolute rounded-sm ${MARKER_STYLES[marker.verdict]} hover:z-10 focus-visible:z-10 focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-bleu-flag dark:focus-visible:outline-paper/60`}
      style={{
        left: `${marker.left * 100}%`,
        width: `${marker.width * 100}%`,
        top: `${marker.lane * LANE_STRIDE_REM}rem`,
        height: `${MARKER_HEIGHT_REM}rem`,
      }}
    >
      <span className="pointer-events-none absolute bottom-full left-1/2 z-20 mb-1.5 hidden w-max max-w-64 -translate-x-1/2 rounded-md bg-night px-2 py-1 text-left text-[11px] leading-4 text-paper shadow-lg group-hover:block group-focus-visible:block">
        {marker.text}
        <span className="mt-0.5 block text-[10px] font-semibold uppercase tracking-wide text-paper/70">
          {t.claims.verdicts[marker.verdict]}
        </span>
      </span>
    </button>
  );
}
