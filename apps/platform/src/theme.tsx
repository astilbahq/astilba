import {
  createContext,
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { flushSync } from "react-dom";

export type ThemePreference = "light" | "dark" | "system";
type Theme = Exclude<ThemePreference, "system">;
type Appearance = { preference: ThemePreference; systemDark: boolean };
const storageKey = "astilba-theme";
const mediaQuery = "(prefers-color-scheme: dark)";

function resolveTheme({ preference, systemDark }: Appearance): Theme {
  return preference === "system" ? (systemDark ? "dark" : "light") : preference;
}

function readPreference(): ThemePreference {
  try {
    const saved = localStorage.getItem(storageKey);
    return saved === "light" || saved === "dark" ? saved : "system";
  } catch {
    // Appearance remains usable when browser storage is disabled.
    return "system";
  }
}

export const ThemeContext = createContext<{
  theme: Theme;
  preference: ThemePreference;
  setPreference: (preference: ThemePreference) => void;
}>({ theme: "light", preference: "system", setPreference: () => {} });

export function ThemeProvider({ children }: { children: ReactNode }) {
  const activeTransition = useRef<ViewTransition | null>(null);
  const [appearance, setAppearance] = useState<Appearance>(() => ({
    preference: readPreference(),
    systemDark: matchMedia(mediaQuery).matches,
  }));
  const requestedAppearance = useRef(appearance);
  const { preference } = appearance;
  const theme = resolveTheme(appearance);

  const changeTheme = useCallback((next: Appearance) => {
    // Keep both choices together while the browser schedules its snapshot callback.
    requestedAppearance.current = next;
    activeTransition.current?.skipTransition();
    activeTransition.current = null;
    const root = document.documentElement;
    root.dataset.themeTransition = "";
    const apply = () => {
      // Capture the complete palette, including React-controlled editor themes.
      flushSync(() => setAppearance(next));
      void getComputedStyle(root).color;
    };
    if (
      root.classList.contains("dark") === (resolveTheme(next) === "dark") ||
      matchMedia("(prefers-reduced-motion: reduce)").matches ||
      !document.startViewTransition
    ) {
      apply();
      root.removeAttribute("data-theme-transition");
      return;
    }

    const transition = document.startViewTransition(() => {
      // Skipping a view transition still invokes its update callback.
      if (activeTransition.current === transition) apply();
    });
    activeTransition.current = transition;
    const finish = () => {
      if (activeTransition.current === transition) {
        activeTransition.current = null;
        root.removeAttribute("data-theme-transition");
      }
    };
    void transition.finished.then(finish, finish);
  }, []);

  useEffect(
    () => () => {
      activeTransition.current?.skipTransition();
      activeTransition.current = null;
      document.documentElement.removeAttribute("data-theme-transition");
    },
    [],
  );

  useEffect(() => {
    const media = matchMedia(mediaQuery);
    const update = () => {
      changeTheme({
        ...requestedAppearance.current,
        systemDark: media.matches,
      });
    };
    const initial = {
      ...requestedAppearance.current,
      systemDark: media.matches,
    };
    requestedAppearance.current = initial;
    setAppearance(initial);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [changeTheme]);

  useLayoutEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
  }, [theme]);

  function setPreference(value: ThemePreference) {
    changeTheme({ ...requestedAppearance.current, preference: value });
    try {
      localStorage.setItem(storageKey, value);
    } catch {
      // Keep the user's choice for this visit even when it cannot be persisted.
    }
  }

  return (
    <ThemeContext value={{ theme, preference, setPreference }}>
      {children}
    </ThemeContext>
  );
}
