"use client";

import { useTransition } from "react";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionaries/fr";
import { setLocalePreference } from "../_actions/set-locale";

const OPTIONS: { locale: Locale; short: string }[] = [
  { locale: "fr", short: "FR" },
  { locale: "en", short: "EN" },
];

// AppLocaleToggle is the analyser's FR/EN switch, styled like the landing's
// LanguageToggle. The app has no locale URL segment, so the options are
// buttons that persist a preference cookie through a Server Action (the
// injection seam default); the action's cookie write makes Next.js re-render
// the page in the chosen language.
export function AppLocaleToggle({
  activeLocale,
  copy,
  action = setLocalePreference,
}: {
  activeLocale: Locale;
  copy: Dictionary["langSwitch"];
  action?: (locale: Locale) => Promise<void>;
}) {
  const [pending, startTransition] = useTransition();

  return (
    <div
      role="group"
      aria-label={copy.label}
      className="inline-flex items-center rounded-full border border-black/10 p-0.5 text-xs font-medium dark:border-white/15"
    >
      {OPTIONS.map(({ locale, short }) => {
        const active = locale === activeLocale;
        return (
          <button
            key={locale}
            type="button"
            lang={locale}
            aria-pressed={active}
            aria-label={locale === "fr" ? copy.toFrench : copy.toEnglish}
            disabled={pending}
            onClick={() => {
              if (active) {
                return;
              }
              startTransition(async () => {
                await action(locale);
              });
            }}
            className={
              active
                ? "rounded-full bg-bleu px-2.5 py-1 text-paper"
                : "rounded-full px-2.5 py-1 text-ink/60 transition-colors hover:text-ink disabled:opacity-60 dark:text-paper/60 dark:hover:text-paper"
            }
          >
            {short}
          </button>
        );
      })}
    </div>
  );
}
