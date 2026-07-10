import { redirect } from "next/navigation";

import { getSession } from "@/lib/auth/session";
import { getDictionary } from "@/lib/i18n/dictionaries";
import { resolveRequestLocale } from "@/lib/i18n/request";

import { BackofficeShell } from "./_components/backoffice-shell";

// Thin async wrapper for /backoffice. Ingestion is an operator task, so the page
// resolves the caller's session and redirects a non-admin to /app server-side -
// a non-admin never receives the backoffice tree. The redirect is UX only; every
// backoffice mutation is independently enforced by the backend admin gate, so a
// forged cookie can at most reveal the chrome, never unlock a mutation. The route
// is private (absent from the proxy's PUBLIC_PATHS), so a cookie-less visitor is
// bounced to /login before this renders.
export default async function BackofficePage() {
  const [{ role, authenticated }, { locale, dict }] = await Promise.all([
    getSession(),
    resolveRequestLocale().then(async (locale) => ({
      locale,
      dict: await getDictionary(locale),
    })),
  ]);
  if (role !== "admin") {
    redirect("/app");
  }
  return (
    <BackofficeShell
      role={role}
      authenticated={authenticated}
      locale={locale}
      dict={dict}
    />
  );
}
