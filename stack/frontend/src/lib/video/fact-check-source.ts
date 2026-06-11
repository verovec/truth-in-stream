import type { VideoKind } from "./api";

// Bridge between the two video identities. The video library API (VER-28)
// deliberately decouples a video's library id (a UUID) from the batch
// processing source (a content-addressed string) and never exposes the object
// key, so the frontend cannot derive a selected video's fact-check source from
// its record. Until live analysis (VER-31) streams verdicts for any selected
// video, curated samples keep their existing batch fact-check demo by mapping
// the known sample title to its processing source. Uploads have no batch source
// and resolve to null (the panel shows a neutral, forward-looking state).
//
// This map is intentionally small and is removed when VER-31 lands.
const SAMPLE_FACT_CHECK_SOURCES: Record<string, string> = {
  "Common Myths": "common-myths.mp4",
};

export function factCheckSourceFor(video: {
  title: string;
  kind: VideoKind;
}): string | null {
  if (video.kind !== "sample") {
    return null;
  }
  return SAMPLE_FACT_CHECK_SOURCES[video.title] ?? null;
}
