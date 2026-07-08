import type { Locale } from "./config";

// PluralForms is the one/other pair dictionaries carry for countable nouns.
// French and English both collapse to these two CLDR categories.
export type PluralForms = { one: string; other: string };

// formatTemplate substitutes {name} placeholders in a dictionary template. An
// unknown placeholder is left intact rather than erased, so a template/vars
// mismatch is visible instead of silently swallowed.
export function formatTemplate(
  template: string,
  vars: Record<string, string | number>,
): string {
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    name in vars ? String(vars[name]) : match,
  );
}

// plural picks the form for a count using the locale's own rule via
// Intl.PluralRules: French counts zero as singular, English does not, so a
// shared n > 1 shortcut would be wrong in one of the two.
export function plural(
  locale: Locale,
  count: number,
  forms: PluralForms,
): string {
  return new Intl.PluralRules(locale).select(count) === "one"
    ? forms.one
    : forms.other;
}
