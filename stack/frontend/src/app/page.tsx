import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { negotiate } from "@/lib/i18n/config";

// The marketing surface lives under /fr and /en. The bare root negotiates the
// visitor's preferred language (French by default) and redirects to it.
export default async function RootPage() {
  const headerList = await headers();
  const locale = negotiate(headerList.get("accept-language"));
  redirect(`/${locale}`);
}
