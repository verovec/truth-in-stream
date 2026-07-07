import Link from "next/link";
import { Brand } from "@/components/marketing/brand";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries";
import { LanguageToggle } from "./language-toggle";

export function Footer({
  locale,
  dict,
}: {
  locale: Locale;
  dict: Dictionary;
}) {
  return (
    <footer className="border-t border-black/5 bg-white/50 dark:border-white/10 dark:bg-white/[0.02]">
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-8 px-6 py-12 sm:flex-row sm:items-start sm:justify-between">
        <div className="max-w-xs">
          <Brand locale={locale} name={dict.brand.name} />
          <p className="mt-3 text-sm text-ink/60 dark:text-paper/60">
            {dict.footer.tagline}
          </p>
          <p className="mt-1 text-sm text-ink/45 dark:text-paper/45">
            {dict.footer.madeIn}
          </p>
        </div>

        <nav className="flex flex-col gap-2 text-sm text-ink/70 dark:text-paper/70">
          {dict.footer.links.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              className="transition-colors hover:text-ink dark:hover:text-paper"
            >
              {link.label}
            </Link>
          ))}
        </nav>

        <div className="flex flex-col items-start gap-4 sm:items-end">
          <LanguageToggle activeLocale={locale} copy={dict.langSwitch} />
          <p className="text-xs text-ink/45 dark:text-paper/45">
            {dict.footer.rights}
          </p>
        </div>
      </div>
    </footer>
  );
}
