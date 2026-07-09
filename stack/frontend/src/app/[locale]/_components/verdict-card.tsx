import type { Dictionary } from "@/lib/i18n/dictionaries";

// The signature element: a spoken claim rendered like a live transcript, with a
// sourced verdict landing beneath it. It is a static showcase of the product's
// core loop (listen, retrieve, verdict), not live data.
export function VerdictCard({ demo }: { demo: Dictionary["hero"]["demo"] }) {
  return (
    <figure className="w-full max-w-md rounded-2xl border border-black/10 bg-white p-5 shadow-xl shadow-bleu/5 dark:border-white/10 dark:bg-white/5 dark:shadow-black/40">
      <div className="flex items-center justify-between gap-3 text-xs">
        <span className="inline-flex items-center gap-1.5 font-medium text-rouge">
          <span className="relative flex h-2 w-2">
            <span className="absolute inline-flex h-full w-full rounded-full bg-rouge opacity-60 motion-safe:animate-ping" />
            <span className="relative inline-flex h-2 w-2 rounded-full bg-rouge" />
          </span>
          {demo.liveLabel}
        </span>
        <span className="font-mono text-ink/50 dark:text-paper/50">
          {demo.speaker} - {demo.timestamp}
        </span>
      </div>

      <blockquote className="mt-3 border-l-2 border-ink/15 pl-3 font-mono text-sm leading-relaxed text-ink dark:border-paper/20 dark:text-paper">
        {demo.claim}
      </blockquote>

      <hr className="my-4 border-black/5 dark:border-white/10" />

      <figcaption className="space-y-3">
        <div className="flex items-center gap-2">
          <span className="text-[0.7rem] font-semibold uppercase tracking-wide text-ink/40 dark:text-paper/40">
            {demo.verdictLabel}
          </span>
          <span className="rounded-full bg-verdict-flag/10 px-2.5 py-1 text-xs font-semibold text-verdict-flag">
            {demo.verdict}
          </span>
        </div>
        <p className="text-sm leading-relaxed text-ink/70 dark:text-paper/70">
          {demo.verdictNote}
        </p>
        <div>
          <p className="text-[0.7rem] font-semibold uppercase tracking-wide text-ink/40 dark:text-paper/40">
            {demo.sourcesLabel}
          </p>
          <ul className="mt-2 flex flex-wrap gap-2">
            {demo.sources.map((source) => (
              <li
                key={source.name}
                className="inline-flex items-baseline gap-1.5 rounded-lg border border-black/10 bg-paper px-2.5 py-1.5 text-xs dark:border-white/10 dark:bg-white/5"
              >
                <span className="font-semibold text-bleu dark:text-sky-300">
                  {source.name}
                </span>
                <span className="text-ink/50 dark:text-paper/50">
                  {source.detail}
                </span>
              </li>
            ))}
          </ul>
        </div>
      </figcaption>
    </figure>
  );
}
