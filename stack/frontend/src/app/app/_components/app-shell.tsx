import { DebugSurface } from "@/components/debug/debug-surface";
import { Brand } from "@/components/marketing/brand";
import { TricoloreRule } from "@/components/marketing/tricolore-rule";
import type { Role } from "@/lib/auth/token";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries/fr";

import { AppI18nProvider } from "@/components/i18n/app-i18n";
import { AppLocaleToggle } from "./app-locale-toggle";
import { LibraryExperience } from "./library-experience";
import { LogoutButton } from "./logout-button";
import { SessionKeepalive } from "./session-keepalive";

// AppShell is the synchronous, testable app surface: the branded header, the
// library experience, the session keepalive, and the admin-gated debug reveal.
// The role, authentication flag, locale, and dictionary are resolved by the
// async page wrapper and passed in, so the shell stays a pure function of its
// props. The locale labels the content wrapper (not <html>, which the shared
// root layout owns) and feeds every client leaf through AppI18nProvider.
// min-w-0 on the column lets the flex children shrink instead of forcing
// horizontal overflow on narrow viewports.
export function AppShell({
  role,
  authenticated,
  locale,
  dict,
}: {
  role: Role;
  authenticated: boolean;
  locale: Locale;
  dict: Dictionary;
}) {
  return (
    <div
      lang={locale}
      className="flex min-w-0 flex-1 flex-col bg-paper font-sans text-ink antialiased dark:bg-night dark:text-paper"
    >
      <AppI18nProvider locale={locale} dict={dict.app}>
        <header className="border-b border-black/5 bg-paper/85 dark:border-white/10 dark:bg-night/85">
          <TricoloreRule />
          <div className="mx-auto flex w-full max-w-6xl items-center justify-between gap-3 px-4 py-3 sm:px-6">
            <Brand locale={locale} name={dict.brand.name} />
            <div className="flex items-center gap-2 sm:gap-3">
              <AppLocaleToggle activeLocale={locale} copy={dict.langSwitch} />
              <LogoutButton label={dict.app.header.signOut} />
            </div>
          </div>
        </header>
        <main className="mx-auto w-full max-w-6xl flex-1 p-4 sm:p-6">
          <LibraryExperience role={role} />
        </main>
        {authenticated && <SessionKeepalive />}
        <DebugSurface role={role} />
      </AppI18nProvider>
    </div>
  );
}
