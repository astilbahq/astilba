import { test } from "@playwright/test";
import { renderStory } from "./story-render";

// A busy browser can run zero-delay timers long after the work queued behind them. A query change
// must still reach components before a response, a retry or a closing dialog that follows it.
for (const id of [
  "pages-account-security--disabled-successfully",
  "pages-environment-purge--purge-without-volumes",
  "pages-environment-retirement--retire-announced",
  "pages-environment-retirement--restore-announced",
  "pages-secret-access--load-more-failed",
])
  test(`${id} completes when zero-delay timers run late`, async ({
    page,
  }, info) => {
    await page.addInitScript(() => {
      if (window.top === window) return;
      const schedule = window.setTimeout;
      window.setTimeout = ((
        handler: TimerHandler,
        delay?: number,
        ...values: unknown[]
      ) =>
        schedule(handler, delay || 100, ...values)) as typeof window.setTimeout;
    });
    await renderStory(
      page,
      `/?path=/story/${id}&globals=a11y.manual:!true`,
      info.timeout,
    );
  });
