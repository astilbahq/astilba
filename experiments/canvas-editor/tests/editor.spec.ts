import { writeFile } from "node:fs/promises";
import { cpus, platform, release, totalmem } from "node:os";
import { resolve } from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { type Configuration, type Layout, storageKey } from "../src/domain";

const draftKey = storageKey("starter", "draft");
const layoutKey = storageKey("starter", "layout");
const stored = (page: Page, key: string) =>
  page.evaluate((key) => localStorage.getItem(key), key);
const node = (page: Page, id: string) =>
  page.locator(`.react-flow__node[data-id="${id}"]`);
async function drag(
  page: Page,
  id: string,
  dx: number,
  dy: number,
  steps = 20,
) {
  const box = await node(page, id).boundingBox();
  if (!box) throw new Error("Service is not visible.");
  await page.mouse.move(box.x + 50, box.y + 40);
  await page.mouse.down();
  await page.mouse.move(box.x + 50 + dx, box.y + 40 + dy, { steps });
  await page.mouse.up();
}

test("initial fit waits for the canvas container after nodes are measured", async ({
  page,
}) => {
  await page.addInitScript(() => {
    const checkVisibility = Object.getOwnPropertyDescriptor(
      Element.prototype,
      "checkVisibility",
    )!.value as Element["checkVisibility"];
    let containerReady = false;
    Element.prototype.checkVisibility = function (options) {
      if (this.classList.contains("react-flow__renderer") && !containerReady)
        return false;
      return checkVisibility.call(this, options);
    };
    window.addEventListener(
      "canvas-measurement-ready",
      () => {
        containerReady = true;
        window.dispatchEvent(new Event("resize"));
      },
      { once: true },
    );
  });
  await page.goto("/");
  // Nodes can finish measuring before the container's resize effect runs.
  await expect(node(page, "svc-web-0")).toBeVisible();
  await page.evaluate(() =>
    window.dispatchEvent(new Event("canvas-measurement-ready")),
  );
  for (const id of [
    "svc-web-0",
    "svc-worker-0",
    "svc-database-0",
    "svc-volume-0",
  ]) {
    await expect(node(page, id)).toBeInViewport({ ratio: 1 });
  }
});

test("pointer editing preserves independent draft and layout saves across reload", async ({
  page,
}) => {
  await page.goto("/");
  await expect(node(page, "svc-web-0")).toBeVisible();
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  const originalDraft = await stored(page, draftKey);
  await page.getByLabel("Service name").fill("Frontend");
  await drag(page, "svc-web-0", 70, 40);
  await page.getByRole("button", { name: "Save layout", exact: true }).click();
  expect(await stored(page, draftKey)).toBe(originalDraft);
  const savedLayout = await stored(page, layoutKey);
  const source = await node(page, "svc-worker-0")
    .locator('[data-handleid="database"]')
    .boundingBox();
  const target = await node(page, "svc-database-0")
    .locator('[data-handleid="input"]')
    .boundingBox();
  if (!source || !target)
    throw new Error("Connection handles are not visible.");
  await page.mouse.move(
    source.x + source.width / 2,
    source.y + source.height / 2,
  );
  await page.mouse.down();
  await page.mouse.move(
    target.x + target.width / 2,
    target.y + target.height / 2,
    { steps: 20 },
  );
  await page.mouse.up();
  await expect(page.getByRole("status")).toHaveText(
    "Connection added to the draft.",
  );
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  expect(await stored(page, layoutKey)).toBe(savedLayout);
  const savedDraft = JSON.parse(
    (await stored(page, draftKey)) ?? "null",
  ) as Configuration;
  expect(savedDraft.services[0].name).toBe("Frontend");
  expect(savedDraft.bindings).toContainEqual({
    source: "svc-worker-0",
    target: "svc-database-0",
    kind: "database",
  });
  await page.reload();
  await expect(page.getByLabel("Service name")).toHaveValue("Frontend");
  await page.getByRole("button", { name: "Save layout", exact: true }).click();
  expect(await stored(page, layoutKey)).toBe(savedLayout);
  await page.getByLabel("Internal port").fill("0");
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  await expect(page.getByLabel("Internal port")).toHaveAttribute(
    "aria-invalid",
    "true",
  );
  expect(JSON.parse((await stored(page, draftKey)) ?? "null")).toEqual(
    savedDraft,
  );
  await page.getByLabel("Internal port").fill("8080");
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  await page.getByText("+ Add service", { exact: true }).click();
  await page.getByRole("button", { name: "Volume", exact: true }).click();
  await expect(page.getByLabel("Service name")).toHaveValue("Volume 1");
  await page.getByLabel("Size (GiB)").fill("20");
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  expect(
    (JSON.parse((await stored(page, draftKey)) ?? "null") as Configuration)
      .services,
  ).toHaveLength(5);
});

test("keyboard, list and mobile paths expose the same editing operations", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Save layout", exact: true }).click();
  const before = JSON.parse(
    (await stored(page, layoutKey)) ?? "null",
  ) as Layout;
  await node(page, "svc-web-0").focus();
  await page.keyboard.press("ArrowRight");
  await page.getByRole("button", { name: "Save layout", exact: true }).click();
  const after = JSON.parse((await stored(page, layoutKey)) ?? "null") as Layout;
  expect(after.positions["svc-web-0"].x).toBeGreaterThan(
    before.positions["svc-web-0"].x,
  );
  await page.getByRole("button", { name: /^zoom in$/i }).click();
  await expect(
    page.getByText("Layout unsaved", { exact: false }),
  ).toBeVisible();
  await page.getByRole("button", { name: /^fit view$/i }).click();
  expect(
    (
      await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
        .analyze()
    ).violations,
  ).toEqual([]);
  await page.getByRole("button", { name: /^Services / }).click();
  await page.getByRole("button", { name: "Edit Worker", exact: true }).focus();
  await page.keyboard.press("Enter");
  await page
    .getByRole("combobox", { name: "Connect to", exact: true })
    .selectOption("svc-database-0");
  await page.getByRole("button", { name: "Connect services" }).click();
  await expect(
    page.getByRole("button", {
      name: "Remove database connection to Database",
    }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Connect services" }).click();
  await expect(page.getByRole("status")).toContainText(
    "already has a database connection",
  );
  await page
    .getByRole("button", { name: "Remove database connection to Database" })
    .click();
  await page.getByLabel("Service name").fill("Queue worker");
  await expect(
    page.getByRole("button", { name: "Edit Queue worker" }),
  ).toBeVisible();
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  expect(
    (
      await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
        .analyze()
    ).violations,
  ).toEqual([]);
  await page.screenshot({
    path: resolve("../../.artifacts/plat1/mobile.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Show on canvas" }).click();
  await expect(node(page, "svc-worker-0")).toBeVisible();
});

test("invalid saved data is recoverable without overwriting it implicitly", async ({
  page,
}) => {
  await page.goto("/");
  await page.evaluate(
    ({ draftKey, layoutKey }) => {
      localStorage.setItem(draftKey, '{"version":9000}');
      localStorage.setItem(layoutKey, "invalid-json");
    },
    { draftKey, layoutKey },
  );
  await page.reload();
  await expect(page.getByRole("alert")).toContainText(
    "Saved draft could not be read",
  );
  await expect(page.getByLabel("Service name")).toHaveValue("Web");
  await page.getByRole("button", { name: "Save layout", exact: true }).click();
  expect(await stored(page, draftKey)).toBe('{"version":9000}');
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  expect(
    (JSON.parse((await stored(page, draftKey)) ?? "null") as Configuration)
      .version,
  ).toBe(1);
});

test("250-service drag records frame costs and isolates committed renders", async ({
  page,
  browserName,
  browser,
}, info) => {
  test.skip(
    browserName !== "chromium",
    "The timing probe uses Chromium's long-task observer.",
  );
  await page.goto("/?profile=1#large");
  await expect(page.locator(".react-flow__node")).toHaveCount(250);
  await page.getByRole("button", { name: /^Services / }).click();
  await page.getByRole("button", { name: "Edit Web 1", exact: true }).click();
  await page.getByRole("button", { name: "Show on canvas" }).click();
  await expect(node(page, "svc-web-0")).toBeInViewport();
  await page.evaluate(
    () =>
      new Promise<void>((done) =>
        requestAnimationFrame(() => requestAnimationFrame(() => done())),
      ),
  );
  await page.evaluate(() => {
    window.__plat1Metrics = { nodes: {}, canvas: 0, properties: 0 };
    const probe = {
      frames: [] as number[],
      longTasks: [] as number[],
      last: 0,
      raf: 0,
      observer: new PerformanceObserver((list) => {
        probe.longTasks.push(
          ...list.getEntries().map((entry) => entry.duration),
        );
      }),
    };
    const tick = (now: number) => {
      if (probe.last) probe.frames.push(now - probe.last);
      probe.last = now;
      probe.raf = requestAnimationFrame(tick);
    };
    probe.observer.observe({ entryTypes: ["longtask"] });
    probe.raf = requestAnimationFrame(tick);
    Object.assign(window, { __plat1Probe: probe });
  });
  await drag(page, "svc-web-0", 160, 100, 120);
  const measured = await page.evaluate(() => {
    const probe = (
      window as unknown as {
        __plat1Probe: {
          frames: number[];
          longTasks: number[];
          raf: number;
          observer: PerformanceObserver;
        };
      }
    ).__plat1Probe;
    cancelAnimationFrame(probe.raf);
    probe.observer.disconnect();
    return {
      frames: probe.frames,
      longTasks: probe.longTasks,
      commits: window.__plat1Metrics,
    };
  });
  expect(measured.commits?.nodes["svc-web-0"]).toBeGreaterThan(1);
  expect(
    Object.entries(measured.commits?.nodes ?? {}).filter(
      ([id]) => id !== "svc-web-0",
    ),
  ).toEqual([]);
  expect(measured.commits?.properties).toBe(0);
  const frames = measured.frames.sort((a, b) => a - b);
  const report = {
    fixture: 250,
    browser: browser.version(),
    viewport: page.viewportSize(),
    hardware: {
      os: platform(),
      release: release(),
      cpu: cpus()[0].model,
      logicalCpus: cpus().length,
      ramGiB: Math.round(totalmem() / 1024 ** 3),
    },
    drag: {
      steps: 120,
      delta: [160, 100],
      frameSamples: frames.length,
      medianMs: frames[Math.floor(frames.length * 0.5)],
      p95Ms: frames[Math.floor(frames.length * 0.95)],
      maxMs: frames.at(-1),
      longTasksMs: measured.longTasks,
      commits: measured.commits,
    },
  };
  await writeFile(
    resolve("../../.artifacts/plat1/performance.json"),
    `${JSON.stringify(report, null, 2)}\n`,
  );
  await info.attach("performance", {
    body: JSON.stringify(report),
    contentType: "application/json",
  });
  await page.getByRole("button", { name: /^fit view$/i }).click();
  await page.screenshot({ path: resolve("../../.artifacts/plat1/large.png") });
});
