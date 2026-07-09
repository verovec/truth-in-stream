"use client";

import { createContext, useContext, type ReactNode } from "react";
import type { Locale } from "@/lib/i18n/config";
import { fr, type Dictionary } from "@/lib/i18n/dictionaries/fr";

// AppDictionary is the analyser's slice of the dictionary. In production the
// server page resolves the active locale and passes only its `app` slice down
// through AppI18nProvider, so the running view renders one locale.
export type AppDictionary = Dictionary["app"];

type AppI18n = { locale: Locale; t: AppDictionary };

// The context default is the French app slice, so a component rendered outside
// the provider (the isolated component tests) still reads coherent French
// strings instead of crashing. This pulls the `fr` dictionary object into the
// /app client bundle even though the provider always overrides it in the real
// app (AppShell wraps the whole view); the cost is a few KB of French UI
// strings behind a fallback production never reaches. Removing it would mean
// requiring the provider everywhere and wrapping every isolated component test,
// which is not worth that saving.
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
