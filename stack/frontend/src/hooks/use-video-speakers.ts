"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  getVideoSpeakers,
  rerunSpeakerNaming,
  type SpeakerUpdate,
  updateVideoSpeaker,
  type VideoSpeaker,
} from "@/lib/video/speakers";

// The naming step starts once an analysis is stored and is bounded to two
// minutes on the backend; the follow-up read covers that window and gives up.
const DEFAULT_NAMING_POLL_MS = 3000;
const NAMING_POLL_ATTEMPTS = 40;

const NO_LABELS: ReadonlySet<string> = new Set();

// SpeakerNamingError names the mutation that failed, for the panel's copy.
export type SpeakerNamingError = "save" | "rerun";

export type SpeakerNaming = {
  speakers: VideoSpeaker[];
  // The labels whose update is in flight.
  savingLabels: ReadonlySet<string>;
  rerunning: boolean;
  error: SpeakerNamingError | null;
  save: (label: string, update: SpeakerUpdate) => void;
  rerun: () => void;
};

// hasProposal reports whether the naming step has written to the mapping: it
// stores one row per voice, named or not, and only its rows are not
// operator-set.
function hasProposal(speakers: VideoSpeaker[]): boolean {
  return speakers.some((speaker) => !speaker.operatorSet);
}

/**
 * Holds the speaker mapping of the video on screen and its two admin
 * mutations. The mapping starts from what the analysis read delivered (loaded)
 * and is replaced in place by a successful update or re-run, so names change on
 * screen without re-fetching the analysis. A new video or a fresh analysis
 * read resets it.
 *
 * awaitNaming covers the session that watched an analysis finish: the names
 * are produced after the completing response was sent, so the mapping is
 * re-read every pollIntervalMs until the naming step's proposals appear or the
 * step's time budget has passed. load, update and rerunNaming are injection
 * seams with production defaults.
 */
export function useVideoSpeakers({
  videoId,
  loaded,
  awaitNaming = false,
  load = getVideoSpeakers,
  update = updateVideoSpeaker,
  rerunNaming = rerunSpeakerNaming,
  pollIntervalMs = DEFAULT_NAMING_POLL_MS,
}: {
  videoId: string | null;
  loaded: VideoSpeaker[];
  awaitNaming?: boolean;
  load?: (videoId: string, signal?: AbortSignal) => Promise<VideoSpeaker[]>;
  update?: (
    videoId: string,
    label: string,
    update: SpeakerUpdate,
  ) => Promise<VideoSpeaker>;
  rerunNaming?: (videoId: string) => Promise<VideoSpeaker[]>;
  pollIntervalMs?: number;
}): SpeakerNaming {
  const [speakers, setSpeakers] = useState(loaded);
  const [savingLabels, setSavingLabels] =
    useState<ReadonlySet<string>>(NO_LABELS);
  const [rerunning, setRerunning] = useState(false);
  const [error, setError] = useState<SpeakerNamingError | null>(null);

  // A different video or a fresh analysis read resets the mapping during
  // render (the derive-state-from-a-changed-prop pattern), so a stale name can
  // never label the newly selected video.
  const [source, setSource] = useState({ videoId, loaded });
  if (source.videoId !== videoId || source.loaded !== loaded) {
    setSource({ videoId, loaded });
    setSpeakers(loaded);
    setSavingLabels(NO_LABELS);
    setRerunning(false);
    setError(null);
  }

  // The video a response belongs to is checked against the one on screen when
  // it lands, so a slow mutation never writes into another video's mapping.
  const videoIdRef = useRef(videoId);
  const loadRef = useRef(load);
  useEffect(() => {
    videoIdRef.current = videoId;
    loadRef.current = load;
  });

  const waiting = videoId !== null && awaitNaming && !hasProposal(speakers);
  useEffect(() => {
    if (videoId === null || !waiting) {
      return;
    }
    let cancelled = false;
    const controller = new AbortController();
    let handle: ReturnType<typeof setTimeout> | undefined;
    let attempts = 0;

    const check = async () => {
      attempts += 1;
      try {
        const next = await loadRef.current(videoId, controller.signal);
        if (cancelled) {
          return;
        }
        if (hasProposal(next)) {
          setSpeakers(next);
          return;
        }
      } catch {
        if (cancelled) {
          return;
        }
      }
      if (attempts < NAMING_POLL_ATTEMPTS) {
        handle = setTimeout(() => void check(), pollIntervalMs);
      }
    };
    handle = setTimeout(() => void check(), pollIntervalMs);

    return () => {
      cancelled = true;
      controller.abort();
      if (handle) {
        clearTimeout(handle);
      }
    };
  }, [videoId, waiting, pollIntervalMs]);

  const save = useCallback(
    (label: string, next: SpeakerUpdate) => {
      if (videoId === null) {
        return;
      }
      const settle = () =>
        setSavingLabels((current) => {
          const remaining = new Set(current);
          remaining.delete(label);
          return remaining;
        });
      setSavingLabels((current) => new Set(current).add(label));
      setError(null);
      update(videoId, label, next)
        .then((saved) => {
          if (videoIdRef.current !== videoId) {
            return;
          }
          setSpeakers((current) =>
            current.some((speaker) => speaker.label === saved.label)
              ? current.map((speaker) =>
                  speaker.label === saved.label ? saved : speaker,
                )
              : [...current, saved],
          );
          settle();
        })
        .catch(() => {
          if (videoIdRef.current !== videoId) {
            return;
          }
          setError("save");
          settle();
        });
    },
    [videoId, update],
  );

  const rerun = useCallback(() => {
    if (videoId === null) {
      return;
    }
    setRerunning(true);
    setError(null);
    rerunNaming(videoId)
      .then((next) => {
        if (videoIdRef.current !== videoId) {
          return;
        }
        setSpeakers(next);
        setRerunning(false);
      })
      .catch(() => {
        if (videoIdRef.current !== videoId) {
          return;
        }
        setError("rerun");
        setRerunning(false);
      });
  }, [videoId, rerunNaming]);

  return { speakers, savingLabels, rerunning, error, save, rerun };
}
