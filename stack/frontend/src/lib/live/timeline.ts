// Positioning math for the claim timeline strip under an analysed video's
// player. Pure: it projects the hydrated statements and their claims onto the
// [0, 1] range of the video duration, so the component only renders the result.
// Markers are laid out in lanes - overlapping markers (the "hot moments"
// density signal) stack vertically instead of covering each other, so every
// marker keeps its own hover and click target.
import type { LiveClaim } from "./claims";
import type { ClaimVerdict } from "./frames";
import type { LiveStatement } from "./statements";

// TimelineMarker is one checked claim placed on the strip. left and width are
// fractions of the video duration; lane is the vertical row the marker occupies
// so overlapping spans never cover each other; seekTo is the playback position
// (seconds) a click jumps to - the claim's statement start, clamped into the
// video.
export type TimelineMarker = {
  claimId: string;
  statementId: string;
  text: string;
  verdict: ClaimVerdict;
  seekTo: number;
  left: number;
  width: number;
  lane: number;
};

// MIN_MARKER_WIDTH keeps a short claim clickable: a marker never renders
// narrower than this fraction of the strip, however brief its statement.
export const MIN_MARKER_WIDTH = 0.012;

// markerVerdict decides whether a claim belongs on the strip and with which
// verdict. Only checked claims mark the timeline: a claim with a concrete
// verdict, or a verified one whose degenerate frame lost it (rendered as
// unverifiable, mirroring the claim list). Pending, checking, unchecked, and
// errored claims return null and leave no mark.
export function markerVerdict(claim: LiveClaim): ClaimVerdict | null {
  if (claim.verdict) {
    return claim.verdict;
  }
  return claim.status === "verified" ? "unverifiable" : null;
}

/**
 * Projects every checked claim onto the strip, positioned by its parent
 * statement's [start, end] seconds against the video duration. Returns no
 * markers until the duration is a positive finite number (metadata not loaded
 * yet), and drops spans that lie entirely outside the video; a span that
 * straddles an edge is clamped into it. Markers come back sorted by position
 * with lanes assigned, ready to render.
 */
export function timelineMarkers(
  statements: readonly LiveStatement[],
  claimsFor: (statementId: string) => LiveClaim[],
  duration: number,
): TimelineMarker[] {
  if (!Number.isFinite(duration) || duration <= 0) {
    return [];
  }
  const markers: TimelineMarker[] = [];
  for (const statement of statements) {
    const { start, end } = statement;
    if (!Number.isFinite(start) || !Number.isFinite(end)) {
      continue;
    }
    if (end <= 0 || start >= duration) {
      continue;
    }
    const clampedStart = Math.max(start, 0);
    const clampedEnd = Math.min(Math.max(end, clampedStart), duration);
    for (const claim of claimsFor(statement.id)) {
      const verdict = markerVerdict(claim);
      if (verdict === null) {
        continue;
      }
      const width = Math.min(
        Math.max((clampedEnd - clampedStart) / duration, MIN_MARKER_WIDTH),
        1,
      );
      markers.push({
        claimId: claim.claimId,
        statementId: statement.id,
        text: claim.text,
        verdict,
        seekTo: clampedStart,
        // A minimum-width marker near the end is pulled back so it never
        // overflows the strip.
        left: Math.min(clampedStart / duration, 1 - width),
        width,
        lane: 0,
      });
    }
  }
  markers.sort((a, b) => a.left - b.left || a.claimId.localeCompare(b.claimId));
  assignLanes(markers);
  return markers;
}

// assignLanes greedily stacks markers: each takes the first lane whose previous
// marker ends at or before its left edge, so only genuinely overlapping markers
// occupy extra lanes and a sparse timeline stays a single row. Mutates the
// sorted markers in place.
function assignLanes(markers: TimelineMarker[]): void {
  const laneEnds: number[] = [];
  for (const marker of markers) {
    const lane = laneEnds.findIndex((endEdge) => marker.left >= endEdge - 1e-9);
    if (lane === -1) {
      marker.lane = laneEnds.length;
      laneEnds.push(marker.left + marker.width);
    } else {
      marker.lane = lane;
      laneEnds[lane] = marker.left + marker.width;
    }
  }
}

/** The number of lanes a marker set occupies, sizing the strip's height. */
export function laneCount(markers: readonly TimelineMarker[]): number {
  let lanes = 0;
  for (const marker of markers) {
    lanes = Math.max(lanes, marker.lane + 1);
  }
  return lanes;
}
