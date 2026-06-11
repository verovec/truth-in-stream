"use client";

import { useLiveAnalysis } from "@/hooks/use-live-analysis";
import type { LiveStatus } from "@/lib/live/session";
import { PlaybackClock } from "./playback-clock";
import { LiveStatementList } from "./live-statement-list";

// LiveFactCheckPanel feeds the fact-check panel from the live analysis stream
// for the selected video: it opens the WebSocket and renders incremental
// subtitles and verdicts as the video plays, keyed to the playback clock.
export function LiveFactCheckPanel({ videoId }: { videoId: string }) {
  const { statements, caption, status } = useLiveAnalysis(videoId);

  return (
    <aside
      aria-labelledby="fact-check-heading"
      className="flex h-full flex-col gap-4 rounded-xl border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-950"
    >
      <header className="flex items-baseline justify-between gap-2">
        <div className="flex items-center gap-2">
          <h2
            id="fact-check-heading"
            className="text-sm font-semibold uppercase tracking-wide text-zinc-900 dark:text-zinc-100"
          >
            Fact checks
          </h2>
          <LiveStatusPill status={status} />
        </div>
        <PlaybackClock />
      </header>
      <ConnectionNotice status={status} />
      {statements.length > 0 ? (
        <LiveStatementList statements={statements} />
      ) : caption ? null : (
        <EmptyHint status={status} />
      )}
      <LiveCaption text={caption} />
    </aside>
  );
}

// LiveCaption shows the current utterance as it is spoken, before it commits to
// a statement, so the transcript is visible word by word rather than appearing
// only when a statement finalizes. It renders nothing between utterances.
function LiveCaption({ text }: { text: string }) {
  if (!text) {
    return null;
  }
  return (
    <p
      aria-live="polite"
      className="mt-auto flex items-start gap-2 border-t border-dashed border-zinc-200 pt-3 text-sm italic leading-5 text-zinc-500 dark:border-zinc-800 dark:text-zinc-400"
    >
      <span
        aria-hidden="true"
        className="mt-1.5 size-1.5 shrink-0 animate-pulse rounded-full bg-rose-500"
      />
      {text}
    </p>
  );
}

// LiveStatusPill is the small live/reconnecting indicator next to the heading.
function LiveStatusPill({ status }: { status: LiveStatus }) {
  if (status === "live") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-rose-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-rose-700 dark:bg-rose-500/15 dark:text-rose-300">
        <span className="size-1.5 animate-pulse rounded-full bg-rose-500" />
        Live
      </span>
    );
  }
  if (status === "reconnecting") {
    return (
      <span className="inline-flex items-center rounded-full bg-amber-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700 dark:bg-amber-500/15 dark:text-amber-300">
        Reconnecting
      </span>
    );
  }
  return null;
}

// ConnectionNotice surfaces interruptions without blocking playback or hiding
// the verdicts already on screen.
function ConnectionNotice({ status }: { status: LiveStatus }) {
  if (status === "error") {
    return (
      <p
        role="alert"
        className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-800 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
      >
        Live analysis was interrupted. Playback continues; press play again to
        retry.
      </p>
    );
  }
  if (status === "reconnecting") {
    return (
      <p className="text-xs text-amber-700 dark:text-amber-400">
        Connection lost. Reconnecting…
      </p>
    );
  }
  return null;
}

// EmptyHint explains, per status, why no statements are shown yet.
function EmptyHint({ status }: { status: LiveStatus }) {
  const message =
    status === "connecting"
      ? "Connecting to live analysis…"
      : status === "live"
        ? "Listening for spoken claims…"
        : status === "ended"
          ? "The stream ended with no checked statements."
          : "Fact checks stream here while the video plays.";
  return <p className="text-sm text-zinc-600 dark:text-zinc-400">{message}</p>;
}
