import Link from "next/link";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries";

type LanguageToggleProps = {
  activeLocale: Locale;
  copy: Dictionary["langSwitch"];
};

const OPTIONS: { locale: Locale; short: string }[] = [
  { locale: "fr", short: "FR" },
  { locale: "en", short: "EN" },
];

// The marketing surface is a single page per locale, so each option links
// straight to the other locale's home. No client state is needed.
export function LanguageToggle({ activeLocale, copy }: LanguageToggleProps) {
  return (
    <nav
      aria-label={copy.label}
      className="inline-flex items-center rounded-full border border-black/10 p-0.5 text-xs font-medium dark:border-white/15"
    >
      {OPTIONS.map(({ locale, short }) => {
        const active = locale === activeLocale;
        return (
          <Link
            key={locale}
            href={`/${locale}`}
            hrefLang={locale}
            aria-current={active ? "true" : undefined}
            aria-label={locale === "fr" ? copy.toFrench : copy.toEnglish}
            className={
              active
                ? "rounded-full bg-bleu px-2.5 py-1 text-paper"
                : "rounded-full px-2.5 py-1 text-ink/60 transition-colors hover:text-ink dark:text-paper/60 dark:hover:text-paper"
            }
          >
            {short}
          </Link>
        );
      })}
    </nav>
  );
}
