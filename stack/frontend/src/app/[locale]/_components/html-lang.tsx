"use client";

import { useEffect } from "react";
import type { Locale } from "@/lib/i18n/config";

// The shared root layout renders <html lang="fr"> (the site default) and cannot
// read the [locale] param without forcing the whole app to render dynamically.
// This leaf corrects the document language for the active locale on the
// marketing surface only.
export function HtmlLang({ locale }: { locale: Locale }) {
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  return null;
}
