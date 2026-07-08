"use client";

import { createContext, useContext, type ReactNode } from "react";
import type { Locale } from "@/lib/i18n/config";
import { fr, type Dictionary } from "@/lib/i18n/dictionaries/fr";

// AppDictionary is the analyser's slice of the dictionary; the server page
// resolves the locale and passes only the active locale's strings across the
// client boundary, so the other locale never ships with the view.
export type AppDictionary = Dictionary["app"];

type AppI18n = { locale: Locale; t: AppDictionary };

// The default is the French app dictionary, matching the product default, so a
// component rendered outside the provider (component tests, storybook-style
// harnesses) still reads coherent French strings instead of crashing.
const AppI18nContext = createContext<AppI18n>({ locale: "fr", t: fr.app });

export function AppI18nProvider({
  locale,
  dict,
  children,
}: {
  locale: Locale;
  dict: AppDictionary;
  children: ReactNode;
}) {
  return (
    <AppI18nContext.Provider value={{ locale, t: dict }}>
      {children}
    </AppI18nContext.Provider>
  );
}

// useAppI18n hands a client component the active locale and the analyser's
// dictionary slice.
export function useAppI18n(): AppI18n {
  return useContext(AppI18nContext);
}
