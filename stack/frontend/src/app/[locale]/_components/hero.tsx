import { CtaLink } from "@/components/marketing/cta-link";
import type { Dictionary } from "@/lib/i18n/dictionaries";
import { VerdictCard } from "./verdict-card";

export function Hero({ hero }: { hero: Dictionary["hero"] }) {
  return (
    <section className="mx-auto grid w-full max-w-6xl grid-cols-1 items-center gap-12 px-6 pb-16 pt-14 sm:pt-20 lg:grid-cols-[1.1fr_1fr] lg:gap-8 lg:pb-24">
      <div>
        <p className="inline-flex items-center gap-2 rounded-full border border-black/10 bg-white/60 px-3 py-1 text-xs font-medium uppercase tracking-wide text-ink/70 dark:border-white/15 dark:bg-white/5 dark:text-paper/70">
          <span className="h-1.5 w-1.5 rounded-full bg-rouge" />
          {hero.eyebrow}
        </p>
        <h1 className="mt-5 text-balance text-4xl font-semibold leading-[1.05] tracking-tight text-ink dark:text-paper sm:text-5xl lg:text-6xl">
          {hero.titleLead}{" "}
          <span className="font-display font-medium italic text-bleu dark:text-sky-300">
            {hero.titleAccent}
          </span>
        </h1>
        <p className="mt-6 max-w-xl text-balance text-lg leading-relaxed text-ink/70 dark:text-paper/70">
          {hero.subtitle}
        </p>
        <div className="mt-8 flex flex-col gap-3 sm:flex-row sm:items-center">
          <CtaLink href="/login">{hero.ctaPrimary}</CtaLink>
          <a
            href="#comment-ca-marche"
            className="inline-flex items-center justify-center gap-1 rounded-lg px-4 py-3 text-sm font-semibold text-ink/70 transition-colors hover:text-ink dark:text-paper/70 dark:hover:text-paper"
          >
            {hero.ctaSecondary}
            <span aria-hidden="true">-&gt;</span>
          </a>
        </div>
      </div>

      <div className="flex justify-center lg:justify-end">
        <VerdictCard demo={hero.demo} />
      </div>
    </section>
  );
}
