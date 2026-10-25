import { createInstance } from "i18next";
import { initReactI18next } from "react-i18next";
import auth from "../locales/en/auth.json";
import common from "../locales/en/common.json";
import { locales, readLocalePreference, resolveLocale } from "./locale";

export const i18n = createInstance();

export async function initializeI18n() {
  const resources = { en: { common, auth } };
  await i18n.use(initReactI18next).init({
    resources,
    lng: "en",
    fallbackLng: "en",
    supportedLngs: locales.map(({ code }) => code),
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
        i18n.addResourceBundle(code, namespace, messages);
      }
    }
  }
  await i18n.changeLanguage(resolveLocale(readLocalePreference()));
}
