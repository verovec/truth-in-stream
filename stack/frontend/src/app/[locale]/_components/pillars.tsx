import { CheckIcon } from "@/components/marketing/check-icon";
import type { Dictionary } from "@/lib/i18n/dictionaries";

export function Pillars({ pillars }: { pillars: Dictionary["pillars"] }) {
  return (
    <section className="border-y border-black/5 bg-white/50 dark:border-white/10 dark:bg-white/[0.02]">
      <div className="mx-auto w-full max-w-6xl px-6 py-14">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-ink/40 dark:text-paper/40">
          {pillars.title}
        </h2>
        <ul className="mt-8 grid grid-cols-1 gap-8 sm:grid-cols-3">
          {pillars.items.map((item) => (
            <li key={item.title}>
              <span className="inline-flex h-8 w-8 items-center justify-center rounded-lg bg-bleu/10 text-bleu dark:bg-sky-300/10 dark:text-sky-300">
                <CheckIcon className="h-4 w-4" />
              </span>
              <h3 className="mt-3 font-semibold text-ink dark:text-paper">
                {item.title}
              </h3>
              <p className="mt-1.5 text-sm leading-relaxed text-ink/65 dark:text-paper/65">
                {item.body}
              </p>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}
