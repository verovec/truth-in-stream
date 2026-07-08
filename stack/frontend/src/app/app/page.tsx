import { getSession } from "@/lib/auth/session";
import { getDictionary } from "@/lib/i18n/dictionaries";
import { resolveRequestLocale } from "@/lib/i18n/request";

import { AppShell } from "./_components/app-shell";

// Thin async wrapper: it resolves the caller's session from the verified Keycloak
// session cookie plus the request locale (preference cookie, then
// Accept-Language) and hands the synchronous, tested AppShell its props. Keeping
// the data reads here and the markup in AppShell makes the shell unit-testable
// (an async Server Component cannot be rendered by the test runner).
export default async function Home() {
  const [{ role, authenticated }, locale] = await Promise.all([
    getSession(),
    resolveRequestLocale(),
  ]);
  const dict = await getDictionary(locale);
  return (
    <AppShell
      role={role}
      authenticated={authenticated}
      locale={locale}
      dict={dict}
    />
  );
}
