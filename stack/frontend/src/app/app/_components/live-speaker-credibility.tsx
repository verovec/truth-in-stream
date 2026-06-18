"use client";

import { useLiveAnalysisSelector } from "@/components/live/live-analysis-provider";
import type { SpeakerCredibility } from "@/lib/live/speakers";

// LiveSpeakerCredibility is the per-speaker running-trust widget on the
// retrieve-then-verify path. It reads the shared live snapshot through a selector
// and renders one row per speaker: a credibility score with the sample size behind
// it. It renders nothing when no speaker scores exist (a legacy stream, or the
// verify path off), so a flag-off session is unaffected.
export function LiveSpeakerCredibility() {
  const speakers = useLiveAnalysisSelector(
    (snapshot) => snapshot?.speakers ?? null,
  );
  return <SpeakerCredibilityView speakers={speakers} />;
}

// thinSampleThreshold is the number of checked claims below which a score is
// visually de-emphasized: with only a claim or two the Bayesian-shrunk score is
// still close to neutral and should not read as a confident judgment.
const thinSampleThreshold = 3;

// SpeakerCredibilityView is the presentational widget. A null or empty list is the
// no-data state (legacy stream or nothing scored yet) and renders nothing.
export function SpeakerCredibilityView({
  speakers,
}: {
  speakers: SpeakerCredibility[] | null;
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

// SpeakerRow is one speaker's score and sample tally. The score colour tracks the
// same positive/negative tones as the verdict badges; a thin sample is muted so a
// near-neutral early score does not read as a verdict. The misleading-framing
// count is a separate affordance from the score: a speaker can be credible overall
// yet repeatedly frame true facts dishonestly, so outright falsehood (which moves
// the score) is shown apart from flagged framing (which does not).
function SpeakerRow({ speaker }: { speaker: SpeakerCredibility }) {
  const checked = speaker.credible + speaker.disputed;
  const thin = checked < thinSampleThreshold;
  const percent = Math.round(speaker.score * 100);
  const tally =
    `${checked} vérifiées` +
    (speaker.unverifiable > 0 ? ` · ${speaker.unverifiable} invérifiables` : "");
  const framing =
    speaker.misleadingFraming > 0
      ? `${speaker.misleadingFraming} cadrage${speaker.misleadingFraming > 1 ? "s" : ""} trompeur${speaker.misleadingFraming > 1 ? "s" : ""}`
      : null;
  const label =
    `Intervenant ${speaker.speaker} : ${percent} % de fiabilité, ${tally}` +
    (framing ? `, ${framing}` : "");
  return (
    <li aria-label={label} className="flex items-baseline gap-2">
      <span className="text-xs font-semibold text-zinc-700 dark:text-zinc-300">
        {speaker.speaker}
      </span>
      <span
        className={`text-base font-semibold tabular-nums ${
          thin ? "text-zinc-400 dark:text-zinc-500" : scoreTone(speaker.score)
        }`}
      >
        {percent} %
      </span>
      <span className="text-[11px] text-zinc-500 dark:text-zinc-400">{tally}</span>
      {framing ? (
        <span className="inline-flex items-center rounded-full bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-800 dark:bg-amber-500/15 dark:text-amber-300">
          {framing}
        </span>
      ) : null}
    </li>
  );
}

// scoreTone maps a credibility score to the verdict palette: a score clearly above
// neutral reads positive, clearly below reads negative, and the band around 0.5 is
// left neutral so a borderline score is not over-claimed.
function scoreTone(score: number): string {
  if (score >= 0.6) {
    return "text-emerald-700 dark:text-emerald-300";
  }
  if (score <= 0.4) {
    return "text-rose-700 dark:text-rose-300";
  }
  return "text-zinc-900 dark:text-zinc-100";
}
