"use client";

import { useLiveAnalysisSelector } from "@/components/live/live-analysis-provider";
import type { SpeakerTally } from "@/lib/live/speakers";

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
  if (speakers === null || speakers.length === 0) {
    return null;
  }
  return (
    <section
      aria-label="Fiabilité des intervenants"
      className="flex w-full flex-col gap-2 rounded-xl border border-zinc-200 bg-white px-4 py-3 dark:border-zinc-800 dark:bg-zinc-950"
    >
      <h2 className="text-sm font-semibold uppercase tracking-wide text-zinc-900 dark:text-zinc-100">
        Fiabilité des intervenants
      </h2>
      <ul className="flex flex-wrap gap-x-6 gap-y-2">
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
  const checkable =
    speaker.credible + speaker.disputed + speaker.unverifiable;
  const parts = [`${speaker.credible} crédibles`, `${speaker.disputed} contestées`];
  if (speaker.unverifiable > 0) {
    parts.push(`${speaker.unverifiable} invérifiables`);
  }
  const breakdown = parts.join(" · ");
  const framing =
    speaker.misleadingFraming > 0
      ? `${speaker.misleadingFraming} cadrage${speaker.misleadingFraming > 1 ? "s" : ""} trompeur${speaker.misleadingFraming > 1 ? "s" : ""}`
      : null;
  const claimsLabel = `${checkable} affirmation${checkable > 1 ? "s" : ""} vérifiable${checkable > 1 ? "s" : ""}`;
  const label =
    `Intervenant ${speaker.speaker} : ${claimsLabel}, ${breakdown}` +
    (framing ? `, ${framing}` : "");
  return (
    <li aria-label={label} className="flex items-baseline gap-2">
      <span className="text-xs font-semibold text-zinc-700 dark:text-zinc-300">
        {speaker.speaker}
      </span>
      <span className="text-xs text-zinc-500 dark:text-zinc-400">
        {claimsLabel}
      </span>
      <span className="text-[11px] text-zinc-500 dark:text-zinc-400">
        {breakdown}
      </span>
      {framing ? (
        <span className="inline-flex items-center rounded-full bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-800 dark:bg-amber-500/15 dark:text-amber-300">
          {framing}
        </span>
      ) : null}
    </li>
  );
}
