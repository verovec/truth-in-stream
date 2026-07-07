import type { Dictionary } from "@/lib/i18n/dictionaries";

// The numbered 01/02/03 markers are used because this is a genuine sequence:
// listen, then retrieve, then verdict. Order carries meaning here.
export function HowItWorks({ how }: { how: Dictionary["how"] }) {
  return (
    <section
      id="comment-ca-marche"
      className="mx-auto w-full max-w-6xl scroll-mt-20 px-6 py-20"
    >
      <p className="text-sm font-semibold uppercase tracking-wide text-rouge">
        {how.eyebrow}
      </p>
      <h2 className="mt-2 max-w-2xl text-3xl font-semibold tracking-tight text-ink dark:text-paper sm:text-4xl">
        {how.title}
      </h2>
      <p className="mt-3 max-w-xl text-ink/65 dark:text-paper/65">
        {how.subtitle}
      </p>
      <ol className="mt-12 grid grid-cols-1 gap-px overflow-hidden rounded-2xl border border-black/10 bg-black/10 dark:border-white/10 dark:bg-white/10 sm:grid-cols-3">
        {how.steps.map((step) => (
          <li
            key={step.index}
            className="flex flex-col bg-paper p-7 dark:bg-night"
          >
            <span className="font-mono text-2xl font-medium text-bleu dark:text-sky-300">
              {step.index}
            </span>
            <h3 className="mt-4 text-lg font-semibold text-ink dark:text-paper">
              {step.title}
            </h3>
            <p className="mt-2 text-sm leading-relaxed text-ink/65 dark:text-paper/65">
              {step.body}
            </p>
          </li>
        ))}
      </ol>
    </section>
  );
}
