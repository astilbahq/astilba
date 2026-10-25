export const locales = [
  { code: "en", name: "English", direction: "ltr", language: "en" },
  ...(import.meta.env.DEV
    ? ([
        {
          code: "en-XA",
          name: "Pseudo (expanded)",
          direction: "ltr",
          language: "en",
        },
        {
          code: "en-XB",
          name: "Pseudo (RTL)",
          direction: "rtl",
          language: "en",
        },
      ] as const)
    : []),
] as const;

export type Locale = (typeof locales)[number]["code"];
export type LocalePreference = Locale | "auto";
const storageKey = "astilba-locale";

export function isLocale(value: string): value is Locale {
  return locales.some((locale) => locale.code === value);
}

export function readLocalePreference(): LocalePreference {
  try {
    const saved = localStorage.getItem(storageKey);
    return saved && isLocale(saved) ? saved : "auto";
  } catch {
    return "auto";
  }
}

export function saveLocalePreference(preference: LocalePreference) {
  try {
    localStorage.setItem(storageKey, preference);
  } catch {
    // A blocked store only limits persistence, not this visit's language.
  }
}

export function resolveLocale(preference: LocalePreference): Locale {
  if (preference !== "auto") return preference;
  // English is currently the only released language. Pseudo is always opt-in.
  return "en";
}
