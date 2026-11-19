"use client";

import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import type { RecapSpeaker } from "@/lib/video/recap";
import { speakerDisplayName } from "./speaker-display-name";

const BUTTON_CLASS =
  "rounded border border-black/10 bg-white px-1.5 py-0.5 text-xs text-ink/80 hover:bg-black/5 disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60";

// SheetOrderControl lets the operator arrange the speaker sheets: one entry
// per principal, in the current order, with a move-up and a move-down button.
// Plain buttons rather than drag-and-drop so it works by keyboard and touch
// alike. It is screen chrome, not report content, so it never prints.
export function SheetOrderControl({
  sheets,
  onMove,
}: {
  sheets: RecapSpeaker[];
  onMove: (index: number, offset: number) => void;
}) {
  const { t } = useAppI18n();
  const copy = t.recap.order;
  if (sheets.length < 2) {
    return null;
  }
  return (
    <section
      aria-labelledby="recap-order-heading"
      className="flex flex-col gap-2 rounded-2xl border border-black/10 bg-white px-5 py-3 dark:border-white/10 dark:bg-white/5 print:hidden"
    >
      <h3
        id="recap-order-heading"
        className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60"
      >
        {copy.heading}
      </h3>
      <ol className="flex flex-col gap-1 sm:flex-row sm:flex-wrap sm:gap-x-4">
        {sheets.map((speaker, index) => {
          const name = speakerDisplayName(speaker, t);
          return (
            <li key={speaker.label} className="flex items-center gap-1.5 text-sm">
              <span className="tabular-nums text-ink/50 dark:text-paper/50">
                {index + 1}.
              </span>
              <span className="mr-1">{name}</span>
              <button
                type="button"
                disabled={index === 0}
                onClick={() => onMove(index, -1)}
                aria-label={formatTemplate(copy.up, { name })}
                className={BUTTON_CLASS}
              >
                <span aria-hidden="true">↑</span>
              </button>
              <button
                type="button"
                disabled={index === sheets.length - 1}
                onClick={() => onMove(index, 1)}
                aria-label={formatTemplate(copy.down, { name })}
                className={BUTTON_CLASS}
              >
                <span aria-hidden="true">↓</span>
              </button>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
