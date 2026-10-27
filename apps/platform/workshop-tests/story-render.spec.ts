import { expect, test } from "@playwright/test";
import { renderStory } from "./story-render";

const restartStory =
  "/?path=/story/pages-hosted-onboarding--restart-request-failure&globals=a11y.manual:!true";

test("story completion survives a load resource outlasting its announcement", async ({
  page,
}, info) => {
  let release!: () => void;
  const resource = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/workshop-load-probe.svg", async (route) => {
    await resource;
    await route.fulfill({
      contentType: "image/svg+xml",
      body: '<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1" />',
    });
  });
  await page.route(
    (url) => url.pathname === "/" && url.searchParams.has("path"),
    async (route) => {
      const response = await route.fetch();
      await route.fulfill({
        response,
        body: (await response.text()).replace(
          "</body>",
          '<img src="/workshop-load-probe.svg" alt="" /></body>',
        ),
      });
    },
  );
  const rendered = renderStory(page, restartStory, info.timeout);
  try {
    const announcement = page.getByText(
      /Component test completed successfully/,
    );
    await expect(announcement).toBeAttached({ timeout: info.timeout });
    await expect(announcement).not.toBeAttached({ timeout: info.timeout });
  } finally {
    release();
  }
  await rendered;
});

test("a failed play assertion cannot count as successful completion", async ({
  page,
}, info) => {
  // Inject a visible-state fault into the real story before its scripts run;
  // its existing toast assertion must reject any nominal channel success.
  await page.route("**/iframe.html?*", async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      body: (await response.text()).replace(
        "</head>",
        '<style>[role="dialog"] { opacity: 0 !important; }</style></head>',
      ),
    });
  });
  await expect(renderStory(page, restartStory, info.timeout)).rejects.toThrow(
    "Component test failed.",
  );
});
