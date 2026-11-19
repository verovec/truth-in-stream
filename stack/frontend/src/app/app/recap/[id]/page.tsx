import { getSession } from "@/lib/auth/session";
import { getDictionary } from "@/lib/i18n/dictionaries";
import { resolveRequestLocale } from "@/lib/i18n/request";
import { ORDER_PARAM } from "@/lib/video/recap";

import { RecapShell } from "./_components/recap-shell";

// Thin async wrapper for /app/recap/[id]: it resolves the route id, the sheet
// order carried by the URL, the caller's session and the request locale
// concurrently, then hands the synchronous, tested RecapShell its props. The
// route is private - absent from the proxy's PUBLIC_PATHS - so an
// unauthenticated visitor is redirected to login before this renders.
export default async function RecapPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ [key: string]: string | string[] | undefined }>;
}) {
  const [{ id }, query, { role, authenticated }, { locale, dict }] =
    await Promise.all([
      params,
      searchParams,
      getSession(),
      resolveRequestLocale().then(async (locale) => ({
        locale,
        dict: await getDictionary(locale),
      })),
    ]);
  const order = query[ORDER_PARAM];
  return (
    <RecapShell
      videoId={id}
      order={typeof order === "string" ? order : null}
      role={role}
      authenticated={authenticated}
      locale={locale}
      dict={dict}
    />
  );
}
