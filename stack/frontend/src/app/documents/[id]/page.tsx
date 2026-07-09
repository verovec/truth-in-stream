import { getSession } from "@/lib/auth/session";
import { getDictionary } from "@/lib/i18n/dictionaries";
import { resolveRequestLocale } from "@/lib/i18n/request";

import { DocumentViewerShell } from "./_components/document-viewer-shell";

// Thin async wrapper for /documents/[id], mirroring /documents: it resolves the
// route id, the caller's session (verified Keycloak cookie), and the request
// locale concurrently, then hands the synchronous, tested DocumentViewerShell its
// props. The route is private - absent from the proxy's PUBLIC_PATHS - so an
// unauthenticated visitor is redirected to login before this renders.
export default async function DocumentViewerPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const [{ id }, { role, authenticated }, { locale, dict }] = await Promise.all(
    [
      params,
      getSession(),
      resolveRequestLocale().then(async (locale) => ({
        locale,
        dict: await getDictionary(locale),
      })),
    ],
  );
  return (
    <DocumentViewerShell
      documentId={id}
      role={role}
      authenticated={authenticated}
      locale={locale}
      dict={dict}
    />
  );
}
