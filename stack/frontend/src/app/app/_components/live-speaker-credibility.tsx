"use client";

import { useLiveAnalysisSelector } from "@/components/live/live-analysis-provider";
import { plural } from "@/lib/i18n/text";
import type { SpeakerTally } from "@/lib/live/speakers";
import { useAppI18n } from "@/components/i18n/app-i18n";

// LiveSpeakerCredibility is the per-speaker breakdown panel on the
// retrieve-then-verify path. It reads the shared live snapshot through a selector
// and renders one row per speaker: an itemised count of the speaker's checkable
// claims and how they broke down. It renders nothing when no speaker tallies exist
// (a legacy stream, or the verify path off), so a flag-off session is unaffected.
export function LiveSpeakerCredibility() {
  const speakers = useLiveAnalysisSelector(
    (snapshot) => snapshot?.speakers ?? null,
  );
  return <SpeakerCredibilityView speakers={speakers} />;
}

// SpeakerCredibilityView is the presentational panel. A null or empty list is the
// no-data state (legacy stream or nothing tallied yet) and renders nothing.
export function SpeakerCredibilityView({
  speakers,
}: {
  speakers: SpeakerTally[] | null;
}) {
  const { t } = useAppI18n();
  if (speakers === null || speakers.length === 0) {
    return null;
  }
  return (
    <section
      aria-label={t.speakers.heading}
      className="flex w-full flex-col gap-2 rounded-2xl border border-black/10 bg-white px-4 py-3 dark:border-white/10 dark:bg-white/5"
    >
      <h2 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
        {t.speakers.heading}
      </h2>
      {/* Capped with internal scroll so a stream that surfaces many speakers
          scrolls this list rather than growing the strip and pushing the layout
          below it down as each new speaker arrives. tabIndex makes the scroll
          region focusable and arrow-scrollable by keyboard - not mouse-wheel or
          touch only - so a keyboard user can still reach speakers past the cap. */}
      <ul
        tabIndex={0}
        className="flex max-h-[4.5rem] flex-wrap gap-x-6 gap-y-2 overflow-y-auto"
      >
        {speakers.map((speaker) => (
          <SpeakerRow key={speaker.speaker} speaker={speaker} />
        ))}
      </ul>
    </section>
  );
}

// SpeakerRow is one speaker's itemised verdict breakdown: how many checkable claims
// they made and the credible / disputed / unverifiable split, plus the
// misleading-framing count. There is no rolled-up trust number: the counts speak
// for themselves. The misleading-framing count is a separate affordance, since a
// speaker can make credible claims yet repeatedly frame true facts dishonestly.
function SpeakerRow({ speaker }: { speaker: SpeakerTally }) {
  const { locale, t } = useAppI18n();
  const checkable =
    speaker.credible + speaker.disputed + speaker.unverifiable;
  const parts = [
    `${speaker.credible} ${plural(locale, speaker.credible, t.speakers.credible)}`,
    `${speaker.disputed} ${plural(locale, speaker.disputed, t.speakers.disputed)}`,
  ];
  if (speaker.unverifiable > 0) {
    parts.push(
      `${speaker.unverifiable} ${plural(locale, speaker.unverifiable, t.speakers.unverifiable)}`,
    );
  }
  const breakdown = parts.join(" · ");
  const framing =
    speaker.misleadingFraming > 0
      ? `${speaker.misleadingFraming} ${plural(locale, speaker.misleadingFraming, t.speakers.framing)}`
      : null;
  const claimsLabel = `${checkable} ${plural(locale, checkable, t.speakers.claim)}`;
  // Comma-joined so the assembled aria-label reads naturally in both locales;
  // the earlier ' : ' baked French space-before-colon spacing into the English
  // screen-reader announcement.
  const label = [`${t.speakers.speaker} ${speaker.speaker}`, claimsLabel, breakdown]
    .concat(framing ? [framing] : [])
    .join(", ");
  return (
    <li aria-label={label} className="flex items-baseline gap-2">
      <span className="text-xs font-semibold text-ink/80 dark:text-paper/80">
        {speaker.speaker}
      </span>
      <span className="text-xs text-ink/50 dark:text-paper/50">
        {claimsLabel}
      </span>
      <span className="text-[11px] text-ink/50 dark:text-paper/50">
        {breakdown}
      </span>
      {framing ? (
        <span className="inline-flex items-center rounded-full bg-verdict-flag/10 px-1.5 py-0.5 text-[10px] font-medium text-verdict-flag dark:bg-verdict-flag/15 dark:text-amber-300">
          {framing}
        </span>
      ) : null}
    </li>
  );
}
