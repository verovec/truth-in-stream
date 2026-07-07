import { CheckIcon } from "@/components/marketing/check-icon";
import { TricoloreRule } from "@/components/marketing/tricolore-rule";
import type { Dictionary } from "@/lib/i18n/dictionaries";

// The civic beat of the page. A deep navy panel, set apart from the paper
// surface, where the "informing is a responsibility" thesis lands.
export function Mission({ mission }: { mission: Dictionary["mission"] }) {
  return (
    <section id="mission" className="mx-auto w-full max-w-6xl scroll-mt-20 px-6 py-10">
      <div className="overflow-hidden rounded-3xl bg-ink text-paper">
        <TricoloreRule />
        <div className="grid grid-cols-1 gap-10 px-8 py-14 lg:grid-cols-[1fr_1fr] lg:px-12">
          <div>
            <p className="text-sm font-semibold uppercase tracking-wide text-paper/50">
              {mission.eyebrow}
            </p>
            <h2 className="mt-3 font-display text-3xl font-medium leading-tight tracking-tight sm:text-4xl">
              {mission.title}
            </h2>
          </div>
          <div>
            <p className="text-lg leading-relaxed text-paper/80">
              {mission.body}
            </p>
            <ul className="mt-6 space-y-2.5">
              {mission.points.map((point) => (
                <li key={point} className="flex items-center gap-3 text-paper/90">
                  <CheckIcon className="h-4 w-4 shrink-0 text-rouge-flag" />
                  {point}
                </li>
              ))}
            </ul>
          </div>
        </div>
      </div>
    </section>
  );
}
