import { DirectionProvider } from "@base-ui/react/direction-provider";
import {
  createContext,
  type ReactNode,
  useContext,
  useLayoutEffect,
  useState,
} from "react";
import { I18nextProvider, useTranslation } from "react-i18next";
import { i18n } from "./instance";
import {
  locales,
  readLocalePreference,
  resolveLocale,
  saveLocalePreference,
  type LocalePreference,
} from "./locale";

const LocaleContext = createContext<{
  preference: LocalePreference;
  setPreference: (preference: LocalePreference) => void;
}>({ preference: "auto", setPreference: () => {} });

function LocaleState({ children }: { children: ReactNode }) {
  const { i18n } = useTranslation();
  const [preference, setPreference] = useState(readLocalePreference);
  const locale =
    locales.find(({ code }) => code === i18n.language) ?? locales[0];
  useLayoutEffect(() => {
    document.documentElement.lang = locale.language;
    document.documentElement.dir = locale.direction;
  }, [locale]);

  return (
    <LocaleContext
      value={{
        preference,
        setPreference(value) {
          // All enabled catalogues are ready before mount; switching does not suspend.
          void i18n.changeLanguage(resolveLocale(value));
          setPreference(value);
          saveLocalePreference(value);
        },
      }}
    >
      <DirectionProvider direction={locale.direction}>
        {children}
      </DirectionProvider>
    </LocaleContext>
  );
}

export function LocaleProvider({ children }: { children: ReactNode }) {
  return (
    <I18nextProvider i18n={i18n}>
      <LocaleState>{children}</LocaleState>
    </I18nextProvider>
  );
}

export function useLocale() {
  return useContext(LocaleContext);
}
