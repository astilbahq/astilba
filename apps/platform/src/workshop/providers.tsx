import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createInstance, type i18n as I18n } from "i18next";
import { useContext, useEffect, useEffectEvent, type ReactNode } from "react";
import { setSession, type Session } from "../api/client";
import { sessionOptions } from "../api/session";
import { ToastProvider } from "../components/toast";
import { TooltipProvider } from "../components/tooltip";
import { initializeI18n } from "../i18n/instance";
import { resolveLocale, type LocalePreference } from "../i18n/locale";
import { LocaleContext, LocaleProvider } from "../i18n/provider";
import { pseudolocalize } from "../i18n/pseudo";
import account from "../locales/en/account.json";
import auth from "../locales/en/auth.json";
import common from "../locales/en/common.json";
import { ThemeContext, ThemeProvider, type ThemePreference } from "../theme";
import { guest } from "./fixtures";

const availableLocales = [
  { code: "en", name: "English", direction: "ltr", language: "en" },
  {
    code: "en-XA",
    name: "Pseudo (expanded)",
    direction: "ltr",
    language: "en",
  },
  { code: "en-XB", name: "Pseudo (RTL)", direction: "rtl", language: "en" },
] as const;

export async function createWorkshopState(session: Session = guest) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Infinity },
      mutations: { retry: false },
    },
  });
  queryClient.setQueryData(sessionOptions.queryKey, setSession(session));
  const instance = createInstance();
  await initializeI18n(
    instance,
    availableLocales.map(({ code }) => code),
  );
  const pseudo = pseudolocalize({ auth, common, account });
  for (const code of ["en-XA", "en-XB"]) {
    for (const [namespace, messages] of Object.entries(pseudo)) {
      instance.addResourceBundle(code, namespace, messages);
    }
  }
  return { queryClient, instance };
}

type CanvasPreferencesProps = {
  theme: ThemePreference;
  locale: LocalePreference;
  onThemeChange: (preference: ThemePreference) => void;
  onLocaleChange: (preference: LocalePreference) => void;
};

function CanvasPreferences({
  children,
  theme,
  locale,
  instance,
  onThemeChange,
  onLocaleChange,
}: CanvasPreferencesProps & { children: ReactNode; instance: I18n }) {
  const appearance = useContext(ThemeContext);
  const setTheme = useEffectEvent(appearance.setPreference);
  useEffect(() => {
    let active = true;
    // ThemeProvider uses flushSync for snapshots; run outside React's effect flush.
    queueMicrotask(() => {
      if (active) setTheme(theme);
    });
    return () => {
      active = false;
    };
  }, [theme]);
  useEffect(() => {
    void instance.changeLanguage(resolveLocale(locale));
  }, [instance, locale]);
  return (
    <ThemeContext
      value={{ ...appearance, preference: theme, setPreference: onThemeChange }}
    >
      <LocaleContext
        value={{ preference: locale, setPreference: onLocaleChange }}
      >
        {children}
      </LocaleContext>
    </ThemeContext>
  );
}

export function WorkshopProviders({
  children,
  state,
  theme,
  locale,
  onThemeChange,
  onLocaleChange,
}: CanvasPreferencesProps & {
  children: ReactNode;
  state: Awaited<ReturnType<typeof createWorkshopState>>;
}) {
  return (
    <LocaleProvider
      instance={state.instance}
      availableLocales={availableLocales}
    >
      <QueryClientProvider client={state.queryClient}>
        <ThemeProvider>
          <CanvasPreferences
            theme={theme}
            locale={locale}
            instance={state.instance}
            onThemeChange={onThemeChange}
            onLocaleChange={onLocaleChange}
          >
            <ToastProvider>
              <TooltipProvider delay={300}>{children}</TooltipProvider>
            </ToastProvider>
          </CanvasPreferences>
        </ThemeProvider>
      </QueryClientProvider>
    </LocaleProvider>
  );
}
