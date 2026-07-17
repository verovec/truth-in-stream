"use client";

import type { ReactNode } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import type { VideoAnalysisTrack } from "@/hooks/use-video-analysis";
import type { Role } from "@/lib/auth/token";
import { formatTemplate } from "@/lib/i18n/text";
import { formatTime } from "@/lib/playback/format-time";
import type { AnalysedLibraryVideo } from "@/lib/video/analysis";

// AnalysisControl is the player-side pre-analysis surface for the selected
// video. An admin can trigger a run on a ready, un-analysed (or failed) video;
// while a run is in flight everyone sees a progress chip fed by the tracker's
// poll; a completed video shows its analysed chip (or a reload affordance when
// the stored result could not be fetched). The admin gate here is presentation
// only - the role comes from the server-verified session and the backend
// enforces RequireAdmin regardless.
export function AnalysisControl({
  role,
  video,
  track,
}: {
  role: Role;
  video: AnalysedLibraryVideo | null;
  track: VideoAnalysisTrack;
}) {
  const { t } = useAppI18n();
  if (!video) {
    return null;
  }

  const admin = role === "admin";
  const canTrigger = admin && video.status === "ready";
  const status = video.analysisStatus;

  if (status === "analysing") {
    return (
      <ControlRow>
        <span
          role="status"
          aria-label={t.analysis.progressAria}
          className="inline-flex items-center gap-2 rounded-full bg-bleu/10 px-3 py-1 text-xs font-semibold text-bleu dark:bg-sky-400/15 dark:text-sky-300"
        >
          <span className="size-1.5 animate-pulse rounded-full bg-bleu dark:bg-sky-300" />
          <span className="tabular-nums">
            {formatTemplate(t.analysis.progress, {
              position: formatTime(track.progressMs / 1000),
            })}
          </span>
        </span>
        <StartError error={track.startError} />
      </ControlRow>
    );
  }

  if (status === "complete") {
    if (track.loadFailed) {
      return (
        <ControlRow>
          <p role="alert" className="text-xs text-rouge dark:text-rose-300">
            {t.analysis.loadError}
          </p>
          <SecondaryButton onClick={track.retryLoad}>
            {t.analysis.reload}
          </SecondaryButton>
        </ControlRow>
      );
    }
    return (
      <ControlRow>
        <span className="inline-flex items-center rounded-full bg-verdict-credible/10 px-3 py-1 text-xs font-semibold text-verdict-credible">
          {t.analysis.complete}
        </span>
      </ControlRow>
    );
  }

  if (status === "failed") {
    return (
      <ControlRow>
        <p role="alert" className="text-xs text-rouge dark:text-rose-300">
          {track.analysisError ?? t.analysis.failed}
        </p>
        {canTrigger && (
          <TriggerButton track={track} label={t.analysis.retry} />
        )}
        <StartError error={track.startError} />
      </ControlRow>
    );
  }

  if (!canTrigger) {
    return null;
  }
  return (
    <ControlRow>
      <TriggerButton track={track} label={t.analysis.analyse} />
      <StartError error={track.startError} />
    </ControlRow>
  );
}

function ControlRow({ children }: { children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-2">{children}</div>
  );
}

function TriggerButton({
  track,
  label,
}: {
  track: VideoAnalysisTrack;
  label: string;
}) {
  const { t } = useAppI18n();
  return (
    <button
      type="button"
      onClick={track.start}
      disabled={track.starting}
      className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
    >
      {track.starting ? t.analysis.starting : label}
    </button>
  );
}

function SecondaryButton({
  onClick,
  children,
}: {
  onClick: () => void;
  children: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="rounded-md border border-black/10 bg-white px-2.5 py-1 text-xs font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
    >
      {children}
    </button>
  );
}

function StartError({ error }: { error: VideoAnalysisTrack["startError"] }) {
  const { t } = useAppI18n();
  if (error === null) {
    return null;
  }
  return (
    <p role="alert" className="text-xs text-rouge dark:text-rose-300">
      {t.analysis.errors[error]}
    </p>
  );
}
