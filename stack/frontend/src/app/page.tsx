import { redirect } from "next/navigation";
import { resolveRequestLocale } from "@/lib/i18n/request";

// The marketing surface lives under /fr and /en. The bare root resolves the
// visitor's preferred language the same way every non-segmented entrypoint does
// - an explicit preference cookie (set by the in-app FR/EN toggle) first, then
// Accept-Language negotiation, French by default - and redirects to it, so a
// language chosen inside the app is honoured on the site's main entrypoint too.
export default async function RootPage() {
  const locale = await resolveRequestLocale();
  redirect(`/${locale}`);
}
