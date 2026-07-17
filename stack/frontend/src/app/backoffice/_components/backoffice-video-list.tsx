"use client";

import { useState } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import { ApiError } from "@/lib/http";
import {
  getVideoAnalysis,
  startVideoAnalysis,
  type BackofficeVideo,
  type VideoAnalysisDetail,
  type VideoAnalysisStatus,
} from "./analysis-api";
import { useVideoAnalysisDetail } from "./use-video-analysis-detail";
import {
  VideoKindBadge,
  VideoStatusBadge,
} from "@/app/app/_components/video-status-badge";

// DeleteError keeps the failure as data (not a rendered string) so an already-
// shown error re-labels itself when the admin switches locales; a failed delete
// keeps the API's own message when it carried one (else null -> generic copy).
type DeleteError = { message: string | null };

// AnalysisFeedback is the analyse trigger's outcome as data for the same
// locale-switch reason: each kind maps to its own dictionary line, so a 409 or
// 422 reads as a clear explanation rather than a raw backend string.
type AnalysisFeedback = "conflict" | "notReady" | "failed";

type StartAnalysis = (id: string, signal?: AbortSignal) => Promise<void>;
type LoadAnalysis = (
  id: string,
  signal?: AbortSignal,
) => Promise<VideoAnalysisDetail>;

// BackofficeVideoList is the admin management list: every video, one row each
// with its title, kind/status/analysis badges, the analyse and re-analyse
// controls, and a two-step delete control. remove performs the deletion;
// onDeleted re-lists the catalog so a removed row leaves. startAnalysis fires
// the pre-analysis trigger and onAnalysisStarted tells the owning section a
// run is live so its polling starts at once.
export function BackofficeVideoList({
  videos,
  remove,
  onDeleted,
  startAnalysis = startVideoAnalysis,
  onAnalysisStarted,
  loadAnalysis = getVideoAnalysis,
  pollIntervalMs,
}: {
  videos: BackofficeVideo[];
  remove: (id: string, signal?: AbortSignal) => Promise<void>;
  onDeleted: () => void;
  startAnalysis?: StartAnalysis;
  onAnalysisStarted: (id: string) => void;
  loadAnalysis?: LoadAnalysis;
  pollIntervalMs: number;
}) {
  const { t } = useAppI18n();
  if (videos.length === 0) {
    return (
      <p className="rounded-xl border border-dashed border-black/15 px-4 py-8 text-center text-sm text-ink/50 dark:border-white/15 dark:text-paper/50">
        {t.backoffice.videos.list.empty}
      </p>
    );
  }
  return (
    <ul className="flex flex-col gap-2">
      {videos.map((video) => (
        <li key={video.id}>
          <BackofficeVideoRow
            video={video}
            remove={remove}
            onDeleted={onDeleted}
            startAnalysis={startAnalysis}
            onAnalysisStarted={onAnalysisStarted}
            loadAnalysis={loadAnalysis}
            pollIntervalMs={pollIntervalMs}
          />
        </li>
      ))}
    </ul>
  );
}

const analysisBadgeBase =
  "inline-flex items-center rounded-full bg-white/85 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide dark:bg-night/80";

const secondaryButtonClass =
  "rounded-md border border-black/10 bg-white px-2.5 py-1 text-xs font-medium text-ink/80 hover:bg-black/5 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60";

const quietButtonClass =
  "rounded-md px-2.5 py-1 text-xs font-medium text-ink/60 hover:bg-black/5 disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:text-paper/60 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60";

// progressPercent turns the run's audio position into a whole percentage of
// the video's duration. Null - an indeterminate label - when the duration is
// unknown (raw uploads) or the first progress read has not landed yet; floored
// and clamped so a run never reads 100 % before it actually completes.
function progressPercent(
  detail: VideoAnalysisDetail | null,
  durationMs: number | null,
): number | null {
  if (detail === null || durationMs === null || durationMs <= 0) {
    return null;
  }
  const pct = Math.floor((100 * detail.analysisProgressMs) / durationMs);
  return Math.min(100, Math.max(0, pct));
}

// formatAnalyzedAt renders the completion timestamp in the active locale,
// falling back to the raw string if it cannot be parsed so a malformed value
// still shows.
function formatAnalyzedAt(analyzedAt: string, locale: string): string {
  const date = new Date(analyzedAt);
  if (Number.isNaN(date.getTime())) {
    return analyzedAt;
  }
  return date.toLocaleString(locale, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

// BackofficeVideoRow mirrors the document viewer's reanalyse confirm pattern:
// two-step confirms (not a native window.confirm) for the destructive actions,
// disabled while a request is in flight, with any API error surfaced inline as
// an alert. It owns only the per-row interactions; the parent owns the catalog,
// its refresh, and the polling that advances the analysis lifecycle.
function BackofficeVideoRow({
  video,
  remove,
  onDeleted,
  startAnalysis,
  onAnalysisStarted,
  loadAnalysis,
  pollIntervalMs,
}: {
  video: BackofficeVideo;
  remove: (id: string, signal?: AbortSignal) => Promise<void>;
  onDeleted: () => void;
  startAnalysis: StartAnalysis;
  onAnalysisStarted: (id: string) => void;
  loadAnalysis: LoadAnalysis;
  pollIntervalMs: number;
}) {
  const { t, locale } = useAppI18n();
  const copy = t.backoffice.videos.list;
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<DeleteError | null>(null);
  const [feedback, setFeedback] = useState<AnalysisFeedback | null>(null);

  // The trigger feedback lives on the row, not in the actions, because a 409
  // flips the row to analysing and unmounts the actions - the explanation must
  // outlive that flip. It is kept exactly while it stays true: a conflict
  // message survives into the analysing state it reported, and any feedback is
  // dropped once the lifecycle moves on (the run resolved, so the last
  // attempt's story no longer describes the row).
  const [feedbackFor, setFeedbackFor] = useState(video.analysisStatus);
  if (feedbackFor !== video.analysisStatus) {
    setFeedbackFor(video.analysisStatus);
    if (!(video.analysisStatus === "analysing" && feedback === "conflict")) {
      setFeedback(null);
    }
  }

  const detail = useVideoAnalysisDetail({
    videoId: video.id,
    analysisStatus: video.analysisStatus,
    analyzedAt: video.analyzedAt,
    loadAnalysis,
    pollIntervalMs,
  });

  const fire = async () => {
    setDeleting(true);
    setError(null);
    try {
      await remove(video.id);
      // onDeleted re-lists; this row unmounts once the video leaves the catalog,
      // so no success-path state reset is needed.
      setConfirming(false);
      onDeleted();
    } catch (err) {
      setConfirming(false);
      setDeleting(false);
      setError({ message: err instanceof Error ? err.message : null });
    }
  };

  return (
    <div className="flex flex-col gap-1.5 rounded-lg border border-black/10 bg-white px-3 py-2 dark:border-white/10 dark:bg-white/5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-ink dark:text-paper">
          {video.title}
        </span>
        <VideoKindBadge kind={video.kind} />
        <VideoStatusBadge status={video.status} />
        <AnalysisBadge
          status={video.analysisStatus}
          pct={progressPercent(detail, video.durationMs)}
        />
        <AnalysisActions
          video={video}
          startAnalysis={startAnalysis}
          onAnalysisStarted={onAnalysisStarted}
          onFeedback={setFeedback}
        />
        {confirming ? (
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-ink/60 dark:text-paper/60">
              {copy.confirm}
            </span>
            <button
              type="button"
              onClick={fire}
              disabled={deleting}
              className="rounded-md border border-rouge/30 bg-rouge/5 px-2.5 py-1 text-xs font-medium text-rouge hover:bg-rouge/10 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-rouge dark:text-rose-300"
            >
              {deleting ? copy.deleting : copy.confirmYes}
            </button>
            <button
              type="button"
              onClick={() => setConfirming(false)}
              disabled={deleting}
              className={quietButtonClass}
            >
              {copy.confirmNo}
            </button>
          </span>
        ) : (
          <button
            type="button"
            onClick={() => setConfirming(true)}
            className={secondaryButtonClass}
          >
            {copy.delete}
          </button>
        )}
      </div>
      <AnalysisSummary video={video} detail={detail} locale={locale} />
      {feedback ? (
        <p role="alert" className="text-xs text-rouge dark:text-rose-300">
          {t.backoffice.videos.list.analysis.errors[feedback]}
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="text-xs text-rouge dark:text-rose-300">
          {error.message === null
            ? copy.deleteErrorFallback
            : formatTemplate(copy.deleteError, { message: error.message })}
        </p>
      ) : null}
    </div>
  );
}

// AnalysisBadge is the at-a-glance analysis state. An un-analysed row stays
// quiet (no badge); an analysing row carries the live percentage when the
// duration is known and an indeterminate label otherwise.
function AnalysisBadge({
  status,
  pct,
}: {
  status: VideoAnalysisStatus;
  pct: number | null;
}) {
  const { t } = useAppI18n();
  const copy = t.backoffice.videos.list.analysis.badge;
  switch (status) {
    case "none":
      return null;
    case "analysing":
      return (
        <span
          className={`${analysisBadgeBase} animate-pulse text-verdict-flag dark:text-amber-300`}
        >
          {pct === null
            ? copy.analysing
            : formatTemplate(copy.analysingPct, { pct })}
        </span>
      );
    case "complete":
      return (
        <span className={`${analysisBadgeBase} text-verdict-credible`}>
          {copy.complete}
        </span>
      );
    case "failed":
      return (
        <span className={`${analysisBadgeBase} text-verdict-disputed`}>
          {copy.failed}
        </span>
      );
  }
}

// AnalysisSummary is the row's second line: completion date and claim
// counters for an analysed video, the stored error for a failed one. The
// counters wait for the per-id detail; until it lands (or if it fails) the
// date alone still tells the operator when the result was produced.
function AnalysisSummary({
  video,
  detail,
  locale,
}: {
  video: BackofficeVideo;
  detail: VideoAnalysisDetail | null;
  locale: string;
}) {
  const { t } = useAppI18n();
  const copy = t.backoffice.videos.list.analysis;
  if (video.analysisStatus === "complete") {
    const parts: string[] = [];
    if (video.analyzedAt !== null) {
      parts.push(
        formatTemplate(copy.analysedOn, {
          date: formatAnalyzedAt(video.analyzedAt, locale),
        }),
      );
    }
    if (detail?.counters) {
      parts.push(formatTemplate(copy.counts, detail.counters));
    }
    if (parts.length === 0) {
      return null;
    }
    return (
      <p className="text-xs text-ink/60 dark:text-paper/60">
        {parts.join(" · ")}
      </p>
    );
  }
  if (video.analysisStatus === "failed") {
    return (
      <p className="text-xs text-rouge dark:text-rose-300">
        {detail?.analysisError
          ? formatTemplate(copy.failedError, { message: detail.analysisError })
          : copy.failedFallback}
      </p>
    );
  }
  return null;
}

// AnalysisActions is the per-row trigger: a direct "Analyse" for a ready
// un-analysed (or failed - nothing of value is overwritten) video, and a
// two-step confirm "Re-analyse" for a completed one, since a re-run replaces
// the stored result. A 409 (someone else already started a run) both explains
// itself and flips the row to analysing - the backend said a run is live -
// while a 422 explains the video is not ready; neither fails silently. The
// feedback is reported to the owning row via onFeedback rather than rendered
// here, so it survives this component unmounting on the analysing flip.
function AnalysisActions({
  video,
  startAnalysis,
  onAnalysisStarted,
  onFeedback,
}: {
  video: BackofficeVideo;
  startAnalysis: StartAnalysis;
  onAnalysisStarted: (id: string) => void;
  onFeedback: (feedback: AnalysisFeedback | null) => void;
}) {
  const { t } = useAppI18n();
  const copy = t.backoffice.videos.list.analysis;
  const [confirming, setConfirming] = useState(false);
  const [starting, setStarting] = useState(false);

  // The confirm step is armed against one analysis state: if polling moves the
  // row (a run started elsewhere, a result landed), the pending confirm no
  // longer describes what firing would do, so it resets during render.
  const [confirmedFor, setConfirmedFor] = useState(video.analysisStatus);
  if (confirmedFor !== video.analysisStatus) {
    setConfirmedFor(video.analysisStatus);
    setConfirming(false);
  }

  if (video.status !== "ready" || video.analysisStatus === "analysing") {
    return null;
  }

  const fire = async () => {
    setStarting(true);
    onFeedback(null);
    try {
      await startAnalysis(video.id);
      setConfirming(false);
      onAnalysisStarted(video.id);
    } catch (err) {
      setConfirming(false);
      if (err instanceof ApiError && err.status === 409) {
        onFeedback("conflict");
        onAnalysisStarted(video.id);
      } else if (err instanceof ApiError && err.status === 422) {
        onFeedback("notReady");
      } else {
        onFeedback("failed");
      }
    } finally {
      setStarting(false);
    }
  };

  if (video.analysisStatus === "complete") {
    return (
      <span className="flex flex-wrap items-center gap-2">
        {confirming ? (
          <>
            <span className="text-xs text-ink/60 dark:text-paper/60">
              {copy.confirm}
            </span>
            <button
              type="button"
              onClick={fire}
              disabled={starting}
              className={secondaryButtonClass}
            >
              {starting ? copy.starting : copy.confirmYes}
            </button>
            <button
              type="button"
              onClick={() => setConfirming(false)}
              disabled={starting}
              className={quietButtonClass}
            >
              {copy.confirmNo}
            </button>
          </>
        ) : (
          <button
            type="button"
            onClick={() => setConfirming(true)}
            className={secondaryButtonClass}
          >
            {copy.reanalyse}
          </button>
        )}
      </span>
    );
  }

  return (
    <button
      type="button"
      onClick={fire}
      disabled={starting}
      className={secondaryButtonClass}
    >
      {starting
        ? copy.starting
        : video.analysisStatus === "failed"
          ? copy.retry
          : copy.analyse}
    </button>
  );
}
