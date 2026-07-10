import { AppHeader } from "@/components/app/app-header";
import { AppI18nProvider } from "@/components/i18n/app-i18n";
import type { Role } from "@/lib/auth/token";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries/fr";

import { SessionKeepalive } from "@/app/app/_components/session-keepalive";
import { DocumentExperience } from "./document-experience";

// DocumentViewerShell is the synchronous, testable viewer surface: the shared app
// header (Documents marked current), the document viewer experience, and the
// session keepalive. The document id, role, authentication flag, locale, and
// dictionary are resolved by the async page wrapper and passed in, so the shell
// stays a pure function of its props and mirrors the documents library shell.
export function DocumentViewerShell({
  documentId,
  role,
  authenticated,
  locale,
  dict,
}: {
  documentId: string;
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
        <AppHeader dict={dict} locale={locale} currentSection="documents" />
        <main className="mx-auto w-full max-w-6xl flex-1 p-4 sm:p-6">
          <DocumentExperience documentId={documentId} role={role} />
        </main>
        {authenticated && <SessionKeepalive />}
      </AppI18nProvider>
    </div>
  );
}
