// The public marketing surface is served under a `[locale]` segment. French is
// the default: `/` negotiates and redirects here, and anything ambiguous or
// unknown resolves to French.
export const locales = ["fr", "en"] as const;

export type Locale = (typeof locales)[number];

export const defaultLocale: Locale = "fr";

export function isLocale(value: string): value is Locale {
  return (locales as readonly string[]).includes(value);
}

// negotiate picks the best supported locale from an Accept-Language header.
// French wins every tie and every ambiguous or unsupported case, so a visitor
// only lands on English when English is expressed as a strictly stronger
// preference.
export function negotiate(acceptLanguage: string | null | undefined): Locale {
  if (!acceptLanguage) {
    return defaultLocale;
  }

  const quality: Record<Locale, number> = { fr: -1, en: -1 };

  for (const part of acceptLanguage.split(",")) {
    const [tag, ...params] = part.trim().split(";");
    const primary = tag.trim().toLowerCase().split("-")[0];
    if (!isLocale(primary)) {
      continue;
    }

    let q = 1;
    for (const param of params) {
      // The q parameter is case-insensitive per RFC 9110; a value of 0 means
      // "not acceptable".
      const match = param.trim().match(/^q=(\d(?:\.\d+)?)$/i);
      if (match) {
        q = Number.parseFloat(match[1]);
      }
    }

    if (q > quality[primary]) {
      quality[primary] = q;
    }
  }

  // A locale is only preferred when its best quality is strictly positive; a
  // sentinel -1 (absent) or an explicit q=0 (rejected) both count as no
  // preference, so French wins by default.
  if (quality.fr <= 0 && quality.en <= 0) {
    return defaultLocale;
  }

  return quality.en > quality.fr ? "en" : "fr";
}
