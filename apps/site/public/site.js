const syncAstilbaThemeControls = () => {
  const currentTheme =
    document.documentElement.dataset.theme === "light" ? "light" : "dark";
  const nextTheme = currentTheme === "light" ? "dark" : "light";

  for (const button of document.querySelectorAll("[data-theme-toggle]")) {
    button.setAttribute("aria-label", `Switch to ${nextTheme} theme`);
    button.setAttribute("title", `Switch to ${nextTheme} theme`);
  }
};

(() => {
  const storageKey = "starlight-theme";
  let theme = "dark";

  try {
    const storedTheme = localStorage.getItem(storageKey);
    if (storedTheme === "light" || storedTheme === "dark") {
      theme = storedTheme;
    } else if (window.matchMedia("(prefers-color-scheme: light)").matches) {
      theme = "light";
    }
  } catch {
    // The dark default remains usable when storage is unavailable.
  }

  document.documentElement.dataset.theme = theme;

  document.addEventListener("DOMContentLoaded", () => {
    syncAstilbaThemeControls();

    for (const button of document.querySelectorAll("[data-theme-toggle]")) {
      button.addEventListener("click", () => {
        const currentTheme =
          document.documentElement.dataset.theme === "light" ? "light" : "dark";
        const nextTheme = currentTheme === "light" ? "dark" : "light";

        document.documentElement.dataset.theme = nextTheme;
        try {
          localStorage.setItem(storageKey, nextTheme);
        } catch {
          // Theme switching still works without persistence.
        }
        syncAstilbaThemeControls();
      });
    }

    const finePointer = window.matchMedia("(hover: hover) and (pointer: fine)");
    for (const link of document.querySelectorAll(
      "[data-design-system-easter-egg]"
    )) {
      link.addEventListener("contextmenu", (event) => {
        if (event.button !== 2 || !finePointer.matches) {
          return;
        }

        event.preventDefault();
        window.location.assign("https://ui.astilba.com/");
      });
    }

    const dialog = document.querySelector("[data-mobile-menu]");
    const opener = document.querySelector("[data-mobile-menu-open]");
    const closer = document.querySelector("[data-mobile-menu-close]");

    if (
      !(dialog instanceof HTMLDialogElement) ||
      !(opener instanceof HTMLElement)
    ) {
      return;
    }

    opener.addEventListener("click", () => dialog.showModal());
    closer?.addEventListener("click", () => dialog.close());
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) {
        dialog.close();
      }
    });
    dialog.addEventListener("close", () => opener.focus());

    for (const link of dialog.querySelectorAll("a")) {
      link.addEventListener("click", () => dialog.close());
    }
  });
})();
