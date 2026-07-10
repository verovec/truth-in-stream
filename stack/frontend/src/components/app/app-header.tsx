import Link from "next/link";
import type { Route } from "next";

import { BrandHeading } from "@/components/marketing/brand-heading";
import { TricoloreRule } from "@/components/marketing/tricolore-rule";
import type { Role } from "@/lib/auth/token";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries/fr";

import { AppLocaleToggle } from "./app-locale-toggle";
import { LogoutButton } from "./logout-button";

// AppSection identifies which product area is showing so the header marks the
// current nav link. Add a section here and a NAV entry to grow the navigation
// by data, not by copying markup.
export type AppSection = "videos" | "documents" | "backoffice";

// NAV is the data the header renders. adminOnly entries appear only for an admin
// caller; the backoffice is operator-only, so a guest never sees it (the backend
// independently enforces every backoffice call, so this is reveal-only chrome).
const NAV: {
  section: AppSection;
  href: Route;
  labelKey: "videos" | "documents" | "backoffice";
  adminOnly?: boolean;
}[] = [
  { section: "videos", href: "/app", labelKey: "videos" },
  { section: "documents", href: "/documents", labelKey: "documents" },
  { section: "backoffice", href: "/backoffice", labelKey: "backoffice", adminOnly: true },
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
  role,
}: {
  dict: Dictionary;
  locale: Locale;
  currentSection: AppSection;
  role: Role;
}) {
  const nav = NAV.filter((item) => !item.adminOnly || role === "admin");
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
            {nav.map((item) => {
              const active = item.section === currentSection;
              const className = `rounded-full px-3 py-1.5 text-sm font-medium transition focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:focus-visible:outline-paper/60 ${
                active
                  ? "bg-bleu/10 text-bleu dark:bg-sky-400/15 dark:text-sky-300"
                  : "text-ink/70 hover:bg-black/5 hover:text-ink dark:text-paper/70 dark:hover:bg-white/10 dark:hover:text-paper"
              }`;
              // The current section renders as an inert span, not a link: a click
              // on the page you are already on must not hard-navigate and tear
              // down an in-progress live-analysis session (the /app WebSocket).
              // Only the other section is a real navigating link.
              return active ? (
                <span
                  key={item.section}
                  aria-current="page"
                  className={className}
                >
                  {dict.app.nav[item.labelKey]}
                </span>
              ) : (
                <Link key={item.section} href={item.href} className={className}>
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
