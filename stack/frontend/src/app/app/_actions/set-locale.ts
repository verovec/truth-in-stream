"use server";

import { cookies } from "next/headers";
import { isLocale } from "@/lib/i18n/config";
import { LOCALE_COOKIE } from "@/lib/i18n/request";

// PREFERENCE_MAX_AGE_SECONDS keeps the language choice for a year; the toggle
// refreshes it on every switch.
const PREFERENCE_MAX_AGE_SECONDS = 60 * 60 * 24 * 365;

// setLocalePreference persists the FR/EN toggle's choice. Mutating the cookie
// in a Server Action makes Next.js re-render the page with the new locale, so
// the whole view switches without a manual reload. The value crosses the wire
// from the client, so it is validated against the supported locales and
// anything else is ignored.
export async function setLocalePreference(locale: string): Promise<void> {
  if (!isLocale(locale)) {
    return;
  }
  (await cookies()).set(LOCALE_COOKIE, locale, {
    path: "/",
    maxAge: PREFERENCE_MAX_AGE_SECONDS,
    sameSite: "lax",
  });
}
