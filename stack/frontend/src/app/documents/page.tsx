import { getSession } from "@/lib/auth/session";
import { getDictionary } from "@/lib/i18n/dictionaries";
import { resolveRequestLocale } from "@/lib/i18n/request";

import { DocumentsShell } from "./_components/documents-shell";

// Thin async wrapper for /documents, mirroring /app: it resolves the caller's
// session (verified Keycloak cookie) and the request locale concurrently and
// hands the synchronous, tested DocumentsShell its props. The route is private -
// it is absent from the proxy's PUBLIC_PATHS, so an unauthenticated visitor is
// redirected to login before this renders.
export default async function DocumentsPage() {
  const [{ role, authenticated }, { locale, dict }] = await Promise.all([
    getSession(),
    resolveRequestLocale().then(async (locale) => ({
      locale,
      dict: await getDictionary(locale),
    })),
  ]);
  return (
    <DocumentsShell
      role={role}
      authenticated={authenticated}
      locale={locale}
      dict={dict}
    />
  );
}
