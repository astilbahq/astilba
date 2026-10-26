import { readFileSync } from "node:fs";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

// The command builds first, so the index is present on a clean checkout too.
const { entries }: { entries: Record<string, { type: string }> } = JSON.parse(
  readFileSync(
    new URL("../../../.artifacts/storybook/static/index.json", import.meta.url),
    "utf8",
  ),
);

for (const [id, entry] of Object.entries(entries)) {
  if (entry.type !== "story") continue;
  test(`${id} renders and completes its interaction`, async ({
    page,
  }, info) => {
    await page.goto(`/?path=/story/${id}&globals=a11y.manual:!true`);
    await expect(
      page.getByText(/Component test completed successfully/),
    ).toBeAttached({ timeout: info.timeout });
    const canvas = page.frameLocator("#storybook-preview-iframe");
    await expect(canvas.getByRole("main")).toBeVisible();
    await expect(
      canvas.getByText("Route not found:", { exact: false }),
    ).toHaveCount(0);
    // Match Storybook's body-level component scope, including portal content.
    const audit = new AxeBuilder({ page })
      .include(["#storybook-preview-iframe", "body"])
      // Empty Base UI sentinels redirect focus; axe reports them as hidden controls.
      // https://github.com/mui/base-ui/issues/4668#issuecomment-4306200868
      .exclude(["#storybook-preview-iframe", "[data-base-ui-focus-guard]"]);
    expect((await audit.analyze()).violations).toEqual([]);
  });
}

// Explicit axe journeys own the scan to avoid concurrent Storybook addon audits.
test("static workshop provides dark RTL auth states without reaching a backend", async ({
  page,
}, info) => {
  const backendRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname.startsWith("/api/") && url.port !== "15201")
      backendRequests.push(url.href);
  });
  await page.setViewportSize({ width: 320, height: 844 });
  await page.goto(
    "/iframe.html?id=pages-two-factor-sign-in--invalid-code&viewMode=story&globals=theme:dark;locale:en-XB;a11y.manual:!true",
  );
  await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
  await expect(page.locator("html")).toHaveClass(/dark/);
  await expect(page.getByRole("group")).toHaveAccessibleDescription(/^⟦/);
  await expect(page.getByRole("group")).toHaveCSS("direction", "ltr");
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({
    path: info.outputPath("mfa-dark-rtl-320.png"),
    fullPage: true,
  });

  const response = await page.evaluate(async () => {
    const response = await fetch("/api/v1/unconfigured-workshop-endpoint");
    return { status: response.status, body: await response.text() };
  });
  expect(response.status).toBe(500);
  expect(response.body).toContain("Missing workshop API fixture");
  expect(backendRequests).toEqual([]);
});

test("switching scenarios resets errors, drafts and session fixtures", async ({
  page,
}, info) => {
  await page.goto("/?path=/story/pages-sign-in--rejected-credentials");
  await expect(
    page.getByText(/Component test completed successfully/),
  ).toBeAttached({ timeout: info.timeout });
  const canvas = page.frameLocator("#storybook-preview-iframe");
  await expect(canvas.getByRole("dialog")).toHaveAccessibleDescription(
    "The email or password is incorrect.",
  );
  await page.getByRole("link", { name: "Default", exact: true }).click();
  await expect(canvas.getByRole("dialog")).toHaveCount(0);
  await expect(canvas.getByLabel("Email", { exact: true })).toHaveValue("");
  await page.getByRole("button", { name: "Invitation", exact: true }).click();
  await page
    .getByRole("link", { name: "Existing Account", exact: true })
    .click();
  await expect(
    canvas.getByText("alex@example.test", { exact: true }),
  ).toBeVisible();
  await page.getByRole("link", { name: "New Account", exact: true }).click();
  await expect(
    canvas.getByRole("textbox", { name: "Your name" }),
  ).toBeVisible();
});

test("canvas and toolbar preferences stay synchronized without losing the form", async ({
  page,
}) => {
  await page.goto("/?path=/story/pages-sign-in--default");
  const canvas = page.frameLocator("#storybook-preview-iframe");
  const email = canvas.getByLabel("Email", { exact: true });
  await email.fill("draft@example.test");
  await canvas.getByRole("button", { name: "Appearance", exact: true }).click();
  await canvas
    .getByRole("menuitemradio", { name: "Dark", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Canvas theme dark", exact: true }),
  ).toBeVisible();
  await expect(canvas.locator("html")).toHaveClass(/dark/);
  await page
    .getByRole("button", { name: "Canvas theme dark", exact: true })
    .click();
  await page.getByRole("option", { name: "light", exact: true }).click();
  await expect(canvas.locator("html")).not.toHaveClass(/dark/);
  await expect(email).toHaveValue("draft@example.test");

  await page
    .getByRole("button", { name: "Canvas language English", exact: true })
    .click();
  await page.getByRole("option", { name: "Pseudo (RTL)", exact: true }).click();
  await expect(canvas.locator("html")).toHaveAttribute("dir", "rtl");
  await expect(canvas.locator('input[name="email"]')).toHaveValue(
    "draft@example.test",
  );
});

test("rejected code feedback translates and stays cleared after editing", async ({
  page,
}, info) => {
  await page.goto("/?path=/story/pages-two-factor-sign-in--invalid-code");
  await expect(
    page.getByText(/Component test completed successfully/),
  ).toBeAttached({ timeout: info.timeout });
  const canvas = page.frameLocator("#storybook-preview-iframe");
  const group = canvas.getByRole("group");
  const slots = group.getByRole("textbox");
  await expect(group).toHaveAccessibleDescription(
    "This authentication code is invalid or has already been used. Try a new code.",
  );
  await page
    .getByRole("button", { name: "Canvas language English", exact: true })
    .click();
  await page.getByRole("option", { name: "Pseudo (RTL)", exact: true }).click();
  await expect(group).toHaveAccessibleDescription(/^⟦/);
  await expect(slots.first()).toHaveValue("1");
  await slots.first().fill("9");
  await expect(group).toHaveAccessibleDescription("");
  for (const slot of await slots.all()) {
    await expect(slot).not.toHaveAttribute("aria-invalid", "true");
  }
  await page
    .getByRole("button", { name: "Canvas language Pseudo (RTL)", exact: true })
    .click();
  await page.getByRole("option", { name: "English", exact: true }).click();
  await expect(group).toHaveAccessibleDescription("");
  await expect(slots.first()).toHaveValue("9");
  await expect(slots.first()).not.toHaveAttribute("aria-invalid", "true");
});

test("sign-in toasts preserve drafts and focus, translate, dismiss and reset on navigation", async ({
  page,
}, info) => {
  await page.goto(
    "/?path=/story/pages-sign-in--rejected-credentials&globals=theme:dark;a11y.manual:!true",
  );
  await expect(
    page.getByText(/Component test completed successfully/),
  ).toBeAttached({ timeout: info.timeout });
  const canvas = page.frameLocator("#storybook-preview-iframe");
  const email = canvas.locator('input[name="email"]');
  const password = canvas.locator('input[name="password"]');
  const notification = canvas.getByRole("dialog");
  await expect(notification).toHaveAccessibleDescription(
    "The email or password is incorrect.",
  );
  await expect(canvas.getByRole("alert")).toHaveCount(0);
  await expect(email).toHaveValue("alex@example.test");
  await expect(password).toHaveValue("correct horse battery staple");
  await email.press("Enter");
  await expect(notification).toHaveCount(1);
  await expect(notification).toBeVisible();
  await expect(email).toBeFocused();
  await page
    .getByRole("button", { name: "Canvas language English", exact: true })
    .click();
  await page.getByRole("option", { name: "Pseudo (RTL)", exact: true }).click();
  await expect(notification).toHaveAccessibleDescription(/^⟦/);
  await expect(notification).toHaveCount(1);
  await expect(canvas.locator("html")).toHaveAttribute("dir", "rtl");
  await expect(canvas.getByRole("region")).toHaveAttribute(
    "aria-live",
    "polite",
  );
  const iframe = page
    .frames()
    .find((frame) => frame.url().includes("iframe.html"))!;
  await page.setViewportSize({ width: 320, height: 844 });
  expect(
    (
      await new AxeBuilder({ page })
        .include("#storybook-preview-iframe")
        .analyze()
    ).violations,
  ).toEqual([]);
  expect(
    await iframe.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  const viewport = canvas.getByRole("region");
  await viewport.click({ position: { x: 6, y: 6 } });
  await expect(viewport).toBeFocused();
  await expect(viewport).toHaveCSS("outline-style", "none");
  await email.press("F6");
  await expect(viewport).toBeFocused();
  await expect(viewport).toHaveCSS("outline-style", "solid");
  await canvas.getByRole("region").press("Tab");
  await expect(notification).toBeFocused();
  await notification.press("Tab");
  const dismiss = notification.getByRole("button");
  await expect(dismiss).toBeFocused();
  await dismiss.press("Enter");
  await expect(notification).toHaveCount(0);
  await expect(email).toBeFocused();
  await page.setViewportSize({ width: 1280, height: 900 });
  await page
    .getByRole("button", { name: "Canvas language Pseudo (RTL)", exact: true })
    .click();
  await page.getByRole("option", { name: "English", exact: true }).click();
  await expect(notification).toHaveCount(0);
  await expect(
    page.getByRole("listbox", { name: "Canvas language" }),
  ).toHaveCount(0);
  await expect(canvas.locator("html")).toHaveAttribute("dir", "ltr");
  await email.press("Enter");
  await expect(notification).toHaveAccessibleDescription(
    "The email or password is incorrect.",
  );
  await page.locator("#pages-sign-in--default").click();
  await expect(notification).toHaveCount(0);
});

for (const reducedMotion of ["no-preference", "reduce"] as const) {
  test(`a toast stack remains scrollable on a short screen with motion ${reducedMotion}`, async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion });
    await page.setViewportSize({ width: 568, height: 240 });
    await page.goto("/iframe.html?id=controls-toast--multiple&viewMode=story");
    const notifications = page.getByRole("dialog");
    await expect(notifications).toHaveCount(3);
    const oldest = notifications.last();
    await expect(oldest.locator("[inert]")).toHaveCount(1);
    await page.getByRole("button", { name: "Show notification" }).press("F6");
    await expect(oldest).toHaveCSS(
      "filter",
      reducedMotion === "reduce" ? "none" : "blur(0px)",
    );
    await expect(oldest.locator("[inert]")).toHaveCount(0);
    const viewport = page.getByRole("region", { name: "Notifications" });
    await expect(viewport).toHaveCSS("overflow-y", "auto");
    await expect
      .poll(() => viewport.evaluate((el) => el.scrollHeight > el.clientHeight))
      .toBe(true);
    for (const notification of await notifications.all()) {
      await notification.scrollIntoViewIfNeeded();
      const bounds = await notification.boundingBox();
      expect(bounds!.y).toBeGreaterThanOrEqual(0);
      expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(240);
    }
  });
}

test("a new toast rises from below the stack without creating scrollbars", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page.goto("/iframe.html?id=controls-toast--default&viewMode=story");
  await expect(page.getByRole("dialog")).toHaveCount(1);
  await expect(page.getByRole("dialog")).toHaveCSS("opacity", "1");
  await page.getByRole("region").evaluate((region) => {
    const previous = region.querySelector('[role="dialog"]');
    type Point = {
      top: number;
      opacity: number;
      blockOverflow: number;
      inlineOverflow: number;
    };
    const state = window as typeof window & {
      toastTrajectory: Promise<Point[]>;
    };
    // Capture the real transition before the click; sample its timeline rather
    // than relying on WebKit painting a particular intermediate frame.
    state.toastTrajectory = new Promise<Point[]>((resolve) => {
      region.addEventListener("transitionrun", function capture(event) {
        const arriving = event.target;
        if (
          !(event instanceof TransitionEvent) ||
          !(arriving instanceof HTMLElement) ||
          arriving === previous ||
          arriving.getAttribute("role") !== "dialog" ||
          event.propertyName !== "opacity"
        ) {
          return;
        }
        const toast = arriving;
        region.removeEventListener("transitionrun", capture);
        const animations = region.getAnimations({ subtree: true });
        const times = animations.map((animation) => animation.currentTime);
        for (const animation of animations) animation.pause();
        const duration = Math.max(
          ...animations.map((animation) => {
            const value = animation.effect?.getComputedTiming().duration;
            return typeof value === "number" ? value : 0;
          }),
        );
        const points: Point[] = [];
        function sample() {
          points.push({
            top: toast.getBoundingClientRect().top,
            opacity: Number(getComputedStyle(toast).opacity),
            blockOverflow: region.scrollHeight - region.clientHeight,
            inlineOverflow: region.scrollWidth - region.clientWidth,
          });
        }
        for (let step = 1; step <= 10; step++) {
          for (const animation of animations) {
            animation.currentTime = (duration * step) / 10;
          }
          sample();
        }
        for (const [index, animation] of animations.entries()) {
          animation.currentTime = times[index];
          animation.play();
        }
        const end = performance.now() + 1000;
        function observe() {
          sample();
          if (performance.now() < end) requestAnimationFrame(observe);
          else resolve(points);
        }
        requestAnimationFrame(observe);
      });
    });
  });
  await page.getByRole("button", { name: "Show notification" }).click();
  const points = await page.evaluate(
    () =>
      (
        window as typeof window & {
          toastTrajectory: Promise<
            {
              top: number;
              opacity: number;
              blockOverflow: number;
              inlineOverflow: number;
            }[]
          >;
        }
      ).toastTrajectory,
  );
  const arrival = points.find(
    (point) => point.opacity > 0.02 && point.opacity < 0.9,
  );
  expect(arrival).toBeDefined();
  expect(arrival!.top).toBeGreaterThan(points.at(-1)!.top + 4);
  expect(Math.max(...points.map((point) => point.blockOverflow))).toBe(0);
  expect(Math.max(...points.map((point) => point.inlineOverflow))).toBe(0);
  await expect(page.getByRole("dialog")).toHaveCount(2);
  await expect(page.getByRole("dialog").first()).toHaveCSS("opacity", "1");
});

test("hover keeps the stack expanded across gaps and excess toasts leave", async ({
  page,
}) => {
  await page.goto("/iframe.html?id=controls-toast--multiple&viewMode=story");
  const notifications = page.getByRole("dialog");
  await expect(notifications).toHaveCount(3);
  const oldestDescription = await notifications
    .last()
    .getAttribute("aria-describedby");
  await notifications.first().hover();
  await expect(notifications.last()).toHaveCSS("filter", "blur(0px)");
  const lower = (await notifications.first().boundingBox())!;
  const upper = (await notifications.nth(1).boundingBox())!;
  await page.mouse.move(
    lower.x + lower.width / 2,
    (lower.y + upper.y + upper.height) / 2,
  );
  await expect(notifications.last().locator("[inert]")).toHaveCount(0);
  await page.mouse.move(0, 0);
  await expect(notifications.last().locator("[inert]")).toHaveCount(1);
  await page.getByRole("button", { name: "Show notification" }).click();
  await expect(
    page.locator(`[aria-describedby="${oldestDescription}"]`),
  ).toHaveCount(0);
  await expect(notifications).toHaveCount(3);
});

test("hosted signup and email verification fit expanded RTL at 320px", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 320, height: 844 });
  for (const id of [
    "pages-hosted-signup--new-account",
    "pages-email-verification--verify",
    "pages-email-verification--email-sent",
    "pages-hosted-onboarding--scan-authenticator",
    "pages-hosted-onboarding--save-recovery-codes",
    "pages-hosted-onboarding--subscription",
  ]) {
    await page.goto(
      `/iframe.html?id=${id}&viewMode=story&globals=theme:dark;locale:en-XB;a11y.manual:!true`,
    );
    await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
    await expect(page.getByRole("heading", { level: 1 })).toContainText("⟦");
    if (id.includes("new-account"))
      await expect(page.locator('input[name="workspace"]')).toBeVisible();
    if (id.endsWith("--verify"))
      await expect(page.locator('button[type="submit"]')).toBeVisible();
    if (id.endsWith("--email-sent"))
      await expect(page.getByRole("status")).toBeVisible();
    if (id.endsWith("--scan-authenticator"))
      await expect(page.getByRole("group")).toBeVisible();
    if (id.endsWith("--save-recovery-codes"))
      await expect(page.locator("li code")).toHaveCount(10);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.screenshot({
      path: info.outputPath(`${id}-dark-rtl-320.png`),
      fullPage: true,
    });
  }
});
