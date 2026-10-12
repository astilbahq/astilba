const DESIGN_SYSTEM_URL = "https://ui.astilba.com/";

const FINE_POINTER_QUERY = "(hover: hover) and (pointer: fine)";
const TARGET_SELECTOR = "[data-design-system-easter-egg], header a.site-title";
const INITIALIZED_ATTRIBUTE = "data-design-system-easter-egg-ready";

export const shouldOpenDesignSystem = ({
  button,
  hasFinePointer,
}: {
  readonly button: number;
  readonly hasFinePointer: boolean;
}): boolean => button === 2 && hasFinePointer;

export const installDesignSystemEasterEgg = (
  root: ParentNode = document,
  targetWindow: Window = window
): void => {
  const finePointer = targetWindow.matchMedia(FINE_POINTER_QUERY);

  for (const link of root.querySelectorAll(TARGET_SELECTOR)) {
    if (
      !(link instanceof HTMLAnchorElement) ||
      link.hasAttribute(INITIALIZED_ATTRIBUTE)
    ) {
      continue;
    }

    link.setAttribute(INITIALIZED_ATTRIBUTE, "");
    link.addEventListener("contextmenu", (event) => {
      if (
        !shouldOpenDesignSystem({
          button: event.button,
          hasFinePointer: finePointer.matches,
        })
      ) {
        return;
      }

      event.preventDefault();
      targetWindow.location.assign(DESIGN_SYSTEM_URL);
    });
  }
};
