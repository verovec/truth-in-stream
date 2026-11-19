import { SessionKeepalive } from "@/app/app/_components/session-keepalive";
import { AppHeader } from "@/components/app/app-header";
import { AppI18nProvider } from "@/components/i18n/app-i18n";
import type { Role } from "@/lib/auth/token";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries/fr";

import { RecapExperience } from "./recap-experience";

// RecapShell is the synchronous, testable recap surface: the shared app header
// (no section marked current, so Videos stays a working link back to the
// library), the report, and the session keepalive. The video id, the requested
// sheet order, the role, the authentication flag, the locale and the dictionary
// are resolved by the async page wrapper and passed in. The header is screen
// chrome and is left out of a printed report.
export function RecapShell({
  videoId,
  order,
  role,
  authenticated,
  locale,
  dict,
}: {
  videoId: string;
  order: string | null;
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
        <div className="print:hidden">
          <AppHeader
            dict={dict}
            locale={locale}
            currentSection="recap"
            role={role}
          />
        </div>
        <main className="mx-auto w-full max-w-5xl flex-1 p-4 sm:p-6">
          {/* Keyed by video so a report's load state and sheet order can never
              carry over to another video's. */}
          <RecapExperience
            key={videoId}
            videoId={videoId}
            brandName={dict.brand.name}
            initialOrder={order}
          />
        </main>
        {authenticated && <SessionKeepalive />}
      </AppI18nProvider>
    </div>
  );
}
