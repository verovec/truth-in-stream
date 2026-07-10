import Link from "next/link";
import type { Route } from "next";

import { BrandHeading } from "@/components/marketing/brand-heading";
import { TricoloreRule } from "@/components/marketing/tricolore-rule";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries/fr";

import { AppLocaleToggle } from "./app-locale-toggle";
import { LogoutButton } from "./logout-button";

// AppSection identifies which product area is showing so the header marks the
// current nav link. Add a section here and a NAV entry to grow the navigation
// by data, not by copying markup.
export type AppSection = "videos" | "documents";

const NAV: { section: AppSection; href: Route; labelKey: "videos" | "documents" }[] =
  [
    { section: "videos", href: "/app", labelKey: "videos" },
    { section: "documents", href: "/documents", labelKey: "documents" },
  ];

// AppHeader is the shared product-app header: the branded wordmark, the primary
// Videos/Documents navigation with the current section marked, the locale toggle
// and sign-out. It is a synchronous server component rendered by each area's
// shell, so the two areas never drift; the locale toggle and logout are the only
// client leaves. currentSection drives the active-link styling and aria-current.
export function AppHeader({
  dict,
  locale,
  currentSection,
}: {
  dict: Dictionary;
  locale: Locale;
  currentSection: AppSection;
}) {
  return (
    <header className="sticky top-0 z-40 border-b border-black/5 bg-paper/85 backdrop-blur-md supports-[backdrop-filter]:bg-paper/70 dark:border-white/10 dark:bg-night/85 dark:supports-[backdrop-filter]:bg-night/70">
      <TricoloreRule />
      <div className="mx-auto flex w-full max-w-6xl items-center justify-between gap-3 px-4 py-3 sm:px-6">
        <div className="flex min-w-0 items-center gap-4 sm:gap-6">
          <BrandHeading name={dict.brand.name} />
          <nav
            aria-label={dict.app.nav.ariaLabel}
            className="flex items-center gap-1"
          >
            {NAV.map((item) => {
              const active = item.section === currentSection;
              return (
                <Link
                  key={item.section}
                  href={item.href}
                  aria-current={active ? "page" : undefined}
                  className={`rounded-full px-3 py-1.5 text-sm font-medium transition focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:focus-visible:outline-paper/60 ${
                    active
                      ? "bg-bleu/10 text-bleu dark:bg-sky-400/15 dark:text-sky-300"
                      : "text-ink/70 hover:bg-black/5 hover:text-ink dark:text-paper/70 dark:hover:bg-white/10 dark:hover:text-paper"
                  }`}
                >
                  {dict.app.nav[item.labelKey]}
                </Link>
              );
            })}
          </nav>
        </div>
        <div className="flex items-center gap-2 sm:gap-3">
          <AppLocaleToggle activeLocale={locale} copy={dict.langSwitch} />
          <LogoutButton label={dict.app.header.signOut} />
        </div>
      </div>
    </header>
  );
}
