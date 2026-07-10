import { AppHeader } from "@/components/app/app-header";
import { AppI18nProvider } from "@/components/i18n/app-i18n";
import type { Role } from "@/lib/auth/token";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries/fr";

import { SessionKeepalive } from "@/app/app/_components/session-keepalive";
import { BackofficeExperience } from "./backoffice-experience";

// BackofficeShell is the synchronous, testable backoffice surface: the shared app
// header (Backoffice marked current), the sectioned operator experience, and the
// session keepalive. The role, authentication flag, locale, and dictionary are
// resolved by the async page wrapper and passed in, so the shell is a pure
// function of its props and mirrors the app and documents shells - one header,
// no drift. The locale labels the content wrapper (not <html>, owned by the root
// layout).
export function BackofficeShell({
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
        <AppHeader
          dict={dict}
          locale={locale}
          currentSection="backoffice"
          role={role}
        />
        <main className="mx-auto w-full max-w-6xl flex-1 p-4 sm:p-6">
          <BackofficeExperience copy={dict.app.backoffice} />
        </main>
        {authenticated && <SessionKeepalive />}
      </AppI18nProvider>
    </div>
  );
}
