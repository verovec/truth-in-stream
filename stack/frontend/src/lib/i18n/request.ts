import "server-only";

import { cookies, headers } from "next/headers";
import { isLocale, negotiate, type Locale } from "./config";

// LOCALE_COOKIE stores the viewer's explicit language choice, set by the app
// header's FR/EN toggle. It is a preference, not a credential.
export const LOCALE_COOKIE = "locale";

// resolveRequestLocale picks the locale for cookie-based surfaces (the
// authenticated /app view and the login page, which have no locale URL
// segment): the preference cookie wins when it names a supported locale,
// otherwise Accept-Language negotiation applies with French as the default.
export async function resolveRequestLocale(): Promise<Locale> {
  const preference = (await cookies()).get(LOCALE_COOKIE)?.value;
  if (preference !== undefined && isLocale(preference)) {
    return preference;
  }
  return negotiate((await headers()).get("accept-language"));
}
