import { expect, type Locator, type Page, test } from "@playwright/test";

// Base UI finishes a change in an animation frame. Holding frames keeps the page as it is
// between the change and that frame, so a test can compare it with the page once the frame ran:
// content still to leave would move what follows it when it goes, and a press can then be released
// over a different element.
async function holdAnimationFrames(page: Page) {
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        const native = window.requestAnimationFrame.bind(window);
        const held: FrameRequestCallback[] = [];
        window.requestAnimationFrame = (callback) => {
          held.push(callback);
          return 0;
        };
        Object.assign(window, {
          releaseAnimationFrames: () =>
            new Promise<void>((released) => {
              window.requestAnimationFrame = native;
              native((time) => {
                for (const callback of held.splice(0)) callback(time);
                released();
              });
            }),
        });
        // Base UI asks for a frame only when it has none pending, so let one already requested
        // run first; everything it asks for from then on is held.
        native(() => resolve());
      }),
  );
  return () =>
    page.evaluate(() =>
      (
        window as unknown as { releaseAnimationFrames: () => Promise<void> }
      ).releaseAnimationFrames(),
    );
}

async function top(locator: Locator) {
  return (await locator.boundingBox())?.y;
}

test("the previous tab panel leaves as the tab changes, not a frame later", async ({
  page,
}) => {
  await page.goto(
    "/iframe.html?id=controls-tabs--default&viewMode=story&globals=a11y.manual:!true",
  );
  const overview = page.getByText("Environment overview.", { exact: true });
  await expect(overview).toBeVisible();
  const release = await holdAnimationFrames(page);
  await page.getByRole("tab", { name: "Configuration", exact: true }).click();
  const configuration = page.getByText("Saved service configuration.", {
    exact: true,
  });
  await expect(configuration).toBeVisible();
  await expect(overview).toBeHidden();
  const before = await top(configuration);
  await release();
  expect(await top(configuration)).toBe(before);
});

test("a resolved field error leaves as the field becomes valid, not a frame later", async ({
  page,
}) => {
  await page.goto(
    "/iframe.html?id=pages-sign-in--validation-errors&viewMode=story&globals=a11y.manual:!true",
  );
  const email = page.getByLabel("Email", { exact: true });
  await expect(email).toHaveAttribute("aria-invalid", "true");
  await expect(
    page.getByText("Enter your email address.", { exact: true }),
  ).toBeVisible();
  const release = await holdAnimationFrames(page);
  await email.fill("owner@example.test");
  await expect(email).not.toHaveAttribute("aria-invalid");
  const password = page.getByLabel("Password", { exact: true });
  const before = await top(password);
  await release();
  expect(await top(password)).toBe(before);
});
