import type { Dictionary } from "@/lib/i18n/dictionaries";
import { ClosingCta } from "./closing-cta";
import { Hero } from "./hero";
import { HowItWorks } from "./how-it-works";
import { Mission } from "./mission";
import { Pillars } from "./pillars";

// The landing body, composed from already-resolved strings. Kept sync and
// presentational so it is unit-testable in both locales; the async page is a
// thin wrapper that loads the dictionary and renders this.
export function Landing({ dict }: { dict: Dictionary }) {
  return (
    <main className="flex flex-1 flex-col">
      <Hero hero={dict.hero} />
      <Pillars pillars={dict.pillars} />
      <HowItWorks how={dict.how} />
      <Mission mission={dict.mission} />
      <ClosingCta closing={dict.closing} />
    </main>
  );
}
