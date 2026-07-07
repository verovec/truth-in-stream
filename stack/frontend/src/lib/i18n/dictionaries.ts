import "server-only";

import { cache } from "react";
import type { Locale } from "./config";
import { fr, type Dictionary } from "./dictionaries/fr";

// French ships in the main bundle (it is the default and most-served locale);
// English is loaded on demand so its strings never weigh down the French page.
const loaders: Record<Locale, () => Promise<Dictionary>> = {
  fr: async () => fr,
  en: async () => (await import("./dictionaries/en")).en,
};

// Cached per request so the layout and page share one load of the dictionary.
export const getDictionary = cache(
  async (locale: Locale): Promise<Dictionary> => loaders[locale](),
);

export type { Dictionary };
