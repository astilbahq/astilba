import { createInstance, type i18n as I18n } from "i18next";
import { initReactI18next } from "react-i18next";
import auth from "../locales/en/auth.json";
import common from "../locales/en/common.json";
import { locales, readLocalePreference, resolveLocale } from "./locale";

export const i18n = createInstance();

export async function initializeI18n(
  instance: I18n = i18n,
  supportedLocales: readonly string[] = locales.map(({ code }) => code),
) {
  const resources = { en: { common, auth } };
  await instance.use(initReactI18next).init({
    resources,
    lng: "en",
    fallbackLng: "en",
    supportedLngs: [...supportedLocales],
    load: "currentOnly",
    defaultNS: "common",
    ns: ["common", "auth"],
    returnEmptyString: false,
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
  if (import.meta.env.DEV) {
    const { pseudolocalize } = await import("./pseudo");
    const pseudo = pseudolocalize(resources.en);
    for (const { code } of locales) {
      if (code === "en") continue;
      for (const [namespace, messages] of Object.entries(pseudo)) {
        instance.addResourceBundle(code, namespace, messages);
      }
    }
  }
  await instance.changeLanguage(resolveLocale(readLocalePreference()));
}
