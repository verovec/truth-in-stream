export type SegmentSpan = {
  start: number;
  end: number;
};

/**
 * Returns the index of the segment whose [start, end) interval contains the
 * given time, or -1 when the time falls before the first segment, in a gap,
 * or at/after the end of the last one. Segments must be sorted by start, as
 * the results API guarantees. Binary search keeps per-tick lookups O(log n).
 */
export function findActiveSegmentIndex(
  segments: readonly SegmentSpan[],
  time: number,
): number {
  let low = 0;
  let high = segments.length - 1;
  let candidate = -1;

  while (low <= high) {
    const mid = (low + high) >> 1;
    if (segments[mid].start <= time) {
      candidate = mid;
      low = mid + 1;
    } else {
      high = mid - 1;
    }
  }

  if (candidate === -1 || time >= segments[candidate].end) {
    return -1;
  }
  return candidate;
}
