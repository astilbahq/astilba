import { expect, test } from "@playwright/test";

test("resting toast dismissal is accessible while rear actions stay hidden until expanded", async ({
  page,
}) => {
  await page.goto(
    "/iframe.html?id=controls-toast--error&viewMode=story&globals=a11y.manual:!true",
  );
  const dismiss = page.getByRole("button", {
    name: "Dismiss notification",
    exact: true,
  });
  await expect(dismiss).toBeVisible();
  await expect(dismiss).not.toBeFocused();
  await page
    .getByRole("button", { name: "Show notification", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(2);
  await expect(dismiss).toHaveCount(1);
  await expect(
    page.getByRole("button", {
      name: "Dismiss notification",
      exact: true,
      includeHidden: true,
    }),
  ).toHaveCount(2);
  await page.keyboard.press("F6");
  await expect(dismiss).toHaveCount(2);
  await expect(
    page.getByRole("region", { name: "Notifications" }),
  ).toHaveAttribute("aria-live", "polite");
});
