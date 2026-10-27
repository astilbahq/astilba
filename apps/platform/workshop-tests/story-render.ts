import { expect, type Page } from "@playwright/test";

export async function renderStory(page: Page, url: string, timeout: number) {
  // State checks do not depend on animation clocks. Dedicated motion journeys
  // keep their own preferences and assertions.
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.addInitScript(() => {
    if (window.top !== window) return;
    // Storybook removes these announcements after seven seconds, potentially
    // before navigation's load event. Retain the actual terminal announcement.
    const observer = new MutationObserver(() => {
      const announcements = Array.from(
        document.querySelectorAll('[data-live-announcer] [role="log"] > div'),
        (node) => node.textContent,
      );
      const terminal =
        announcements.find(
          (text) =>
            text === "Component test failed." ||
            text === "Component test was aborted.",
        ) ??
        announcements.find(
          (text) => text === "Component test completed successfully.",
        );
      if (terminal) {
        document.documentElement.dataset.workshopResult = terminal;
        observer.disconnect();
      }
    });
    observer.observe(document, {
      childList: true,
      subtree: true,
      characterData: true,
    });
  });
  await page.goto(url);
  await expect(page.locator("html[data-workshop-result]")).toBeAttached({
    timeout,
  });
  const result = await page
    .locator("html")
    .getAttribute("data-workshop-result");
  if (result !== "Component test completed successfully.") {
    throw new Error(`Storybook: ${result}`);
  }
}
