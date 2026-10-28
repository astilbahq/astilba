import { expect, test, type APIResponse } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";

const workerOrigin =
  process.env.ASTILBA_TEST_WORKER_ORIGIN ?? "http://127.0.0.1:15183";

function htmlNonce(response: APIResponse, body?: string): string {
  expect(response.status()).toBe(200);
  const headers = response.headers();
  const nonce =
    headers["content-security-policy"]?.match(/'nonce-([^']+)'/)?.[1];
  expect(nonce).toBeTruthy();
  expect(headers["content-security-policy"]).toBe(
    `default-src 'self'; script-src 'self'; style-src 'self' 'nonce-${nonce}'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'`,
  );
  expect(headers["x-content-type-options"]).toBe("nosniff");
  expect(headers["referrer-policy"]).toBe("no-referrer");
  expect(headers["cache-control"]).toBe("no-store");
  for (const name of ["etag", "last-modified", "content-range"])
    expect(headers[name]).toBeUndefined();
  if (body !== undefined)
    expect(body).toContain(`<meta name="style-nonce" content="${nonce}">`);
  return nonce!;
}

test("Worker HTML nonce survives navigation, HEAD and asset conditionals without changing static caching", async ({
  request,
}) => {
  const first = await request.get(`${workerOrigin}/`);
  const body = await first.text();
  const nonce = htmlNonce(first, body);
  for (const path of ["/account", "/index.html"]) {
    const response = await request.get(`${workerOrigin}${path}`);
    expect(htmlNonce(response, await response.text())).not.toBe(nonce);
  }
  const conditionals: Record<string, string>[] = [
    { "If-None-Match": "*" },
    { Range: "bytes=0-10" },
    { Range: "bytes=999999999-" },
  ];
  for (const headers of conditionals) {
    const response = await request.get(`${workerOrigin}/`, { headers });
    expect(htmlNonce(response, await response.text())).not.toBe(nonce);
  }
  const head = await request.head(`${workerOrigin}/`, {
    headers: { "If-None-Match": "*" },
  });
  htmlNonce(head);
  expect(await head.body()).toHaveLength(0);

  const script = body.match(/<script[^>]+src="([^"]+)"/)?.[1];
  expect(script).toBeTruthy();
  const asset = await request.get(new URL(script!, workerOrigin).href);
  expect(asset.status()).toBe(200);
  expect(asset.headers()["cache-control"]).not.toBe("no-store");
  const etag = asset.headers().etag;
  expect(etag).toBeTruthy();
  const cached = await request.get(new URL(script!, workerOrigin).href, {
    headers: { "If-None-Match": etag },
  });
  expect(cached.status()).toBe(304);
  const partial = await request.get(new URL(script!, workerOrigin).href, {
    headers: { Range: "bytes=0-10" },
  });
  expect(partial.headers().etag).toBe(etag);
  if (partial.status() === 206) {
    expect(partial.headers()["content-range"]).toMatch(/^bytes 0-10\//);
    expect(await partial.body()).toEqual((await asset.body()).subarray(0, 11));
  } else {
    expect(partial.status()).toBe(200);
    expect(await partial.body()).toEqual(await asset.body());
  }

  const preparation = await request.get(
    `${workerOrigin}/onboarding/network-prepare.sh`,
  );
  expect(preparation.status()).toBe(200);
  expect(preparation.headers()["content-type"]).toContain("text/x-shellscript");
  expect(preparation.headers()["cache-control"]).toBe("no-store");
  expect(await preparation.text()).toMatch(/^#!/);
});

test("Worker CSP permits the Compose editor's nonce stylesheet", async ({
  page,
}, info) => {
  await page.addInitScript(() => {
    addEventListener("securitypolicyviolation", (event) => {
      if (["style-src", "style-src-elem"].includes(event.effectiveDirective))
        document.documentElement.dataset.stylesheetViolation =
          event.effectiveDirective;
    });
  });
  await page.goto(workerOrigin);
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("button", { name: "New project", exact: true }).click();
  const name = `Worker styles ${info.project.name} ${crypto.randomUUID()}`;
  await page.getByLabel("Project name", { exact: true }).fill(name);
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await page.getByRole("link", { name: `${name} Open project` }).click();
  await page
    .getByRole("link", { name: "New environment", exact: true })
    .click();
  await page.getByLabel("Environment name", { exact: true }).fill("Files");
  await page
    .getByLabel("Configuration", { exact: true })
    .selectOption("compose");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
  await openEnvironmentSection(page, "Configuration");
  const editor = page.getByRole("textbox", {
    name: "Source compose.yaml",
    exact: true,
  });
  await editor.fill("services:\n  web:\n    image: busybox:1.37.0\n");
  await expect(
    page.getByText("Source validated.", { exact: true }),
  ).toBeVisible();
  expect(
    await page
      .locator(".cm-editor")
      .evaluate((element) => getComputedStyle(element).position),
  ).toBe("relative");
  const styles = await page.evaluate(() => {
    const nonce = document.querySelector<HTMLMetaElement>(
      'meta[name="style-nonce"]',
    )?.content;
    return {
      nonce,
      adopted: [...document.querySelectorAll("style")].some(
        (style) => style.nonce === nonce && style.sheet !== null,
      ),
    };
  });
  expect(styles.nonce).toBeTruthy();
  expect(styles.adopted).toBe(true);
  await expect(page.locator("html")).not.toHaveAttribute(
    "data-stylesheet-violation",
  );
});
