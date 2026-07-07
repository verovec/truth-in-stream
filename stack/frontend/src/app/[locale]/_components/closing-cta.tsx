import Link from "next/link";
import type { Dictionary } from "@/lib/i18n/dictionaries";

export function ClosingCta({ closing }: { closing: Dictionary["closing"] }) {
  return (
    <section className="mx-auto w-full max-w-6xl px-6 pb-24 pt-10">
      <div className="rounded-3xl border border-black/10 bg-white px-6 py-16 text-center dark:border-white/10 dark:bg-white/5">
        <h2 className="mx-auto max-w-2xl text-balance font-display text-3xl font-medium tracking-tight text-ink dark:text-paper sm:text-4xl">
          {closing.title}
        </h2>
        <p className="mx-auto mt-4 max-w-xl text-ink/70 dark:text-paper/70">
          {closing.body}
        </p>
        <Link
          href="/login"
          className="mt-8 inline-flex items-center justify-center rounded-lg bg-bleu px-6 py-3 text-sm font-semibold text-paper transition-colors hover:bg-bleu/90"
        >
          {closing.cta}
        </Link>
      </div>
    </section>
  );
}
