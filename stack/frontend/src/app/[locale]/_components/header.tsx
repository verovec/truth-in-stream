import { Brand } from "@/components/marketing/brand";
import { CtaLink } from "@/components/marketing/cta-link";
import { TricoloreRule } from "@/components/marketing/tricolore-rule";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries";
import { LanguageToggle } from "./language-toggle";

export function Header({
  locale,
  dict,
}: {
  locale: Locale;
  dict: Dictionary;
}) {
  return (
    <header className="sticky top-0 z-40 border-b border-black/5 bg-paper/85 backdrop-blur dark:border-white/10 dark:bg-night/85">
      <TricoloreRule />
      <div className="mx-auto flex w-full max-w-6xl items-center justify-between gap-4 px-6 py-3.5">
        <Brand locale={locale} name={dict.brand.name} />

        <nav className="hidden items-center gap-6 text-sm font-medium text-ink/70 dark:text-paper/70 md:flex">
          <a
            href="#comment-ca-marche"
            className="transition-colors hover:text-ink dark:hover:text-paper"
          >
            {dict.nav.howItWorks}
          </a>
          <a
            href="#mission"
            className="transition-colors hover:text-ink dark:hover:text-paper"
          >
            {dict.nav.mission}
          </a>
        </nav>

        <div className="flex items-center gap-3">
          <LanguageToggle activeLocale={locale} copy={dict.langSwitch} />
          <CtaLink href="/login" size="sm">
            {dict.nav.openApp}
          </CtaLink>
        </div>
      </div>
    </header>
  );
}
