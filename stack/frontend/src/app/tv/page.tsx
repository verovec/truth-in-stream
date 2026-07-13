import { getSession } from "@/lib/auth/session";
import { getDictionary } from "@/lib/i18n/dictionaries";
import { resolveRequestLocale } from "@/lib/i18n/request";

import { TvShell } from "./_components/tv-shell";

// Thin async wrapper for /tv, mirroring /app and /documents: it resolves the
// caller's session (verified Keycloak cookie) and the request locale
// concurrently and hands the synchronous, tested TvShell its props. The route is
// private - it is absent from the proxy's PUBLIC_PATHS, so an unauthenticated
// visitor is redirected to login before this renders.
export default async function TvPage() {
  const [{ role, authenticated }, { locale, dict }] = await Promise.all([
    getSession(),
    resolveRequestLocale().then(async (locale) => ({
      locale,
      dict: await getDictionary(locale),
    })),
  ]);
  return (
    <TvShell
      role={role}
      authenticated={authenticated}
      locale={locale}
      dict={dict}
    />
  );
}
