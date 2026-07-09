// Card styling for the fact-check region's rows. The subtitle region renders as
// a borderless continuous transcript, so these boxed styles are now scoped to
// the fact-check list alone, where a selected row is emphasized over the base.
export const LIVE_ROW_BASE_CLASS =
  "border-black/10 bg-white dark:border-white/10 dark:bg-white/5";

export const LIVE_ROW_EMPHASIZED_CLASS =
  "border-bleu-flag/60 bg-bleu-flag/5 dark:border-sky-400/60 dark:bg-sky-400/10";

// The canonical evidence/source link treatment (bleu with an underline that
// darkens on hover; sky in dark mode), shared by the citation links in a match
// row and the primary-source link above them so the two never drift apart.
// Callers add layout utilities (min-w-0, break-words, text size) as needed.
export const SOURCE_LINK_CLASS =
  "font-medium text-bleu underline decoration-bleu/30 underline-offset-2 hover:decoration-bleu dark:text-sky-300 dark:decoration-sky-300/40 dark:hover:decoration-sky-300";
