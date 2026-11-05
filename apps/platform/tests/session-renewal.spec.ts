import { randomUUID } from "node:crypto";
import {
  createServer,
  type IncomingHttpHeaders,
  request as forward,
} from "node:http";
import type { AddressInfo } from "node:net";
import { expect, type Page, test } from "@playwright/test";
import { authFixtureSQL } from "./auth-fixture";

const api = "127.0.0.1:15181";

// Holding the reply in a real proxy keeps the browser's own abort and cookie handling under test.
function renewalProxy() {
  let hold = false;
  let release = () => {};
  let markHeld = () => {};
  const held = new Promise<void>((resolve) => {
    markHeld = resolve;
  });
  const server = createServer((incoming, outgoing) => {
    const headers: IncomingHttpHeaders = { ...incoming.headers, host: api };
    if (headers.origin) headers.origin = `http://${api}`;
    const upstream = forward(
      {
        host: "127.0.0.1",
        port: 15181,
        path: incoming.url,
        method: incoming.method,
        headers,
        agent: false,
      },
      (response) => {
        const send = () => {
          outgoing.writeHead(response.statusCode ?? 502, response.headers);
          response.pipe(outgoing);
        };
        if (hold && incoming.url === "/api/v1/auth/password") {
          hold = false;
          release = send;
          markHeld();
        } else send();
      },
    );
    upstream.on("error", () => outgoing.destroy());
    incoming.pipe(upstream);
  });
  return {
    server,
    held,
    holdRenewal: () => {
      hold = true;
    },
    releaseRenewal: () => release(),
  };
}

let proxy: ReturnType<typeof renewalProxy>;
let origin: string;

test.beforeEach(async () => {
  proxy = renewalProxy();
  await new Promise<void>((resolve) =>
    proxy.server.listen(0, "127.0.0.1", resolve),
  );
  origin = `http://127.0.0.1:${(proxy.server.address() as AddressInfo).port}`;
});

test.afterEach(async () => {
  proxy.server.closeAllConnections();
  await new Promise((resolve) => proxy.server.close(resolve));
});

async function createAccount() {
  const account = randomUUID();
  const email = `${account}@example.test`;
  await authFixtureSQL(
    `INSERT INTO accounts(id,email,name,password_hash) SELECT '${account}','${email}','Session renewal test',password_hash FROM accounts WHERE email='owner@example.test'`,
  );
  return email;
}

async function signIn(page: Page, email: string) {
  await page.goto(`${origin}/login?returnTo=/account`);
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Account security", exact: true }),
  ).toBeVisible();
}

// Once this returns the server has replaced the token, but the browser has no new cookie.
async function holdPasswordChange(page: Page) {
  await page
    .getByRole("button", { name: "Change password", exact: true })
    .click();
  await page
    .getByLabel("New password", { exact: true })
    .fill("renewed private browser password");
  proxy.holdRenewal();
  await page
    .getByRole("button", { name: "Update password", exact: true })
    .click();
  await proxy.held;
}

async function readSession(page: Page) {
  const reply = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/v1/session",
  );
  await page.clock.setSystemTime(Date.now() + 20000);
  await page.evaluate(() =>
    document.dispatchEvent(new Event("visibilitychange", { bubbles: true })),
  );
  return (await (await reply).json()) as { user?: { email: string } };
}

test("a session read carrying the replaced token cannot abort its renewal", async ({
  page,
}) => {
  await signIn(page, await createAccount());
  await holdPasswordChange(page);
  expect((await readSession(page)).user).toBe(undefined);
  // An abort only has to beat the reply, so leave it time to happen first.
  await page.waitForTimeout(250);
  proxy.releaseRenewal();

  await expect(page.getByRole("status")).toHaveText(
    "Password updated. Your other sessions have been signed out.",
  );
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Account security", exact: true }),
  ).toBeVisible();
});

test("signing in as another account still stops a renewal in flight", async ({
  context,
  page,
}) => {
  await signIn(page, await createAccount());
  await holdPasswordChange(page);
  const other = await createAccount();
  await signIn(await context.newPage(), other);
  expect((await readSession(page)).user?.email).toBe(other);
  await expect(
    page.getByRole("main").getByText(other, { exact: true }),
  ).toBeVisible();
  await page.waitForTimeout(250);
  proxy.releaseRenewal();

  await page.waitForTimeout(250);
  await page.reload();
  await expect(
    page.getByRole("main").getByText(other, { exact: true }),
  ).toBeVisible();
});
