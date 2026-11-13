import { randomUUID } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { authFixtureSQL } from "./auth-fixture";
import { openEnvironmentSection } from "./environment-section";
import { signInAsOwner } from "./fixtures/owner-sign-in";
import { api } from "./fixtures/ports";

async function deployedEnvironment(page: Page, name: string) {
  const session = await (await page.request.get("/api/v1/session")).json();
  const workspaceId: string = session.workspaces[0].id;
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: api,
  };
  const root = `/api/v1/workspaces/${workspaceId}`;
  const post = async (url: string, data: unknown) => {
    const response = await page.request.post(url, { headers, data });
    expect(response.status()).toBe(201);
    return response.json();
  };
  const project = await post(`${root}/projects`, { name });
  const projects = `${root}/projects/${project.id}/environments`;
  const environment = await post(projects, { name: `${name} environment` });
  const applicationId = randomUUID();
  const environmentPath = `${projects}/${environment.id}`;
  expect(
    (
      await page.request.put(`${environmentPath}/draft`, {
        headers,
        data: {
          baseRevisionId: environment.draftRevisionId,
          specification: {
            version: 1,
            variables: [],
            applications: [
              {
                id: applicationId,
                name: "Worker",
                kind: "worker",
                source: { kind: "image", image: "redis:alpine" },
              },
            ],
          },
        },
      })
    ).status(),
  ).toBe(200);
  const enrollment = await post(`${root}/servers`, {
    name: `${name} server`,
    role: "deployment",
  });
  return {
    workspaceId,
    userId: session.user.id as string,
    headers,
    environmentPath,
    environmentId: environment.id as string,
    projectId: project.id as string,
    applicationId,
    serverId: enrollment.server.id as string,
  };
}
type Deployed = Awaited<ReturnType<typeof deployedEnvironment>>;

// Opening a session needs an enrolled agent and a deployed container, which this
// suite does not run. Listing and closing act on these rows through the real API.
async function seedSession(
  deployed: Deployed,
  actor: string,
  instance: number,
) {
  await authFixtureSQL(
    `INSERT INTO troubleshooting_sessions(id,workspace_id,environment_id,unit_id,server_id,application_id,instance,actor_id,auth_version,browser_session_hash,controller_generation,state,ticket_expires_at,deadline_at)
     SELECT '${randomUUID()}',d.workspace_id,d.environment_id,d.id,'${deployed.serverId}','${deployed.applicationId}',${instance},'${actor}',1,'\\x00',1,'active',now(),now()+interval '1 hour'
     FROM deployment_units d WHERE d.workspace_id='${deployed.workspaceId}' AND d.environment_id='${deployed.environmentId}' AND d.primary_unit`,
  );
}
async function seedOperator(workspaceId: string, name: string) {
  const id = randomUUID();
  await authFixtureSQL(
    `INSERT INTO accounts(id,email,name,password_hash) SELECT '${id}','${id}@example.test','${name}',password_hash FROM accounts WHERE email='owner@example.test'; INSERT INTO memberships(workspace_id,account_id,role) VALUES('${workspaceId}','${id}','operator')`,
  );
  return id;
}

test("owner controls terminal sessions, ends them and reviews their history", async ({
  page,
}, info) => {
  await page.goto("/");
  await signInAsOwner(page);
  await expect(
    page.getByRole("button", { name: /^Account menu for/ }),
  ).toBeVisible();
  const name = `Terminal ${info.project.name} ${randomUUID().slice(0, 8)}`;
  const deployed = await deployedEnvironment(page, name);
  const environmentURL = `/?workspace=${deployed.workspaceId}&project=${deployed.projectId}&environment=${deployed.environmentId}`;
  await page.goto(environmentURL);
  await openEnvironmentSection(page, "Settings");
  const panel = page.getByRole("region", {
    name: "Terminal sessions",
    exact: true,
  });
  await expect(panel).toContainText("No sessions are open");
  await expect(
    panel.getByText("This installation has not enabled", { exact: false }),
  ).toHaveCount(0);

  const allow = panel.getByLabel("Allow terminal sessions in this environment");
  const restrict = panel.getByLabel("Only owners can open sessions");
  const save = panel.getByRole("button", { name: "Save setting", exact: true });
  await expect(allow).not.toBeChecked();
  await expect(save).toBeDisabled();
  await allow.check();
  await restrict.check();
  await save.click();
  await expect(panel.getByRole("status")).toHaveText(
    "Terminal session setting saved.",
  );
  await page.reload();
  await openEnvironmentSection(page, "Settings");
  await expect(allow).toBeChecked();
  await expect(restrict).toBeChecked();

  // Another owner saves while this owner is editing: the draft survives the conflict.
  await restrict.uncheck();
  const saved = await page.request.get(
    `${deployed.environmentPath}/troubleshooting`,
  );
  const { generation } = await saved.json();
  expect(
    (
      await page.request.put(`${deployed.environmentPath}/troubleshooting`, {
        headers: deployed.headers,
        data: { enabled: false, ownersOnly: true, generation },
      })
    ).status(),
  ).toBe(200);
  await save.click();
  await expect(panel.getByRole("alert")).toContainText(
    "Your choices are kept here",
  );
  await expect(allow).toBeChecked();
  await expect(restrict).not.toBeChecked();
  await expect(save).toBeDisabled();
  await panel
    .getByRole("button", { name: "Load current setting", exact: true })
    .click();
  await expect(allow).not.toBeChecked();
  await expect(restrict).toBeChecked();
  await expect(
    panel.getByRole("heading", { name: "Access in this environment" }),
  ).toBeFocused();

  const operator = await seedOperator(deployed.workspaceId, "Sam Operator");
  await seedSession(deployed, deployed.userId, 1);
  await seedSession(deployed, operator, 2);
  const endOwn = panel.getByRole("button", {
    name: "End session on Worker, instance 1",
    exact: true,
  });
  await expect(endOwn).toBeVisible({ timeout: 10000 });
  await expect(panel).toContainText("Sam Operator", { timeout: 10000 });
  await expect(panel).toContainText(`${name} server`);
  await new AxeBuilder({ page }).analyze().then(({ violations }) => {
    expect(violations).toEqual([]);
  });

  await endOwn.click();
  await page
    .getByRole("alertdialog")
    .getByRole("button", { name: "End session", exact: true })
    .click();
  await expect(panel.getByRole("status")).toHaveText("Session ended.");
  await expect(endOwn).toHaveCount(0);
  await expect(
    panel.getByRole("heading", { name: "Open sessions" }),
  ).toBeFocused();

  await panel
    .getByRole("button", {
      name: "End all sessions in this workspace",
      exact: true,
    })
    .click();
  const endAll = page.getByRole("alertdialog");
  await expect(endAll).toContainText("every environment of this workspace");
  await endAll
    .getByRole("button", { name: "End all sessions", exact: true })
    .click();
  await expect(panel.getByRole("status")).toHaveText(
    "All sessions in this workspace ended.",
  );
  await expect(panel).toContainText("No sessions are open");

  // Sessions are off in this environment now, so the API refuses and audits this.
  const refused = await page.request.post(
    `${deployed.environmentPath}/troubleshooting/sessions`,
    {
      headers: deployed.headers,
      data: { applicationId: deployed.applicationId, instance: 1 },
    },
  );
  expect((await refused.json()).code).toBe("troubleshooting_refused");

  await page.getByRole("link", { name: "Workspace", exact: true }).click();
  await expect(
    page.getByText(
      "Terminal session refused: An owner has not allowed terminal sessions in this environment.",
      { exact: true },
    ),
  ).not.toHaveCount(0);
  const history = page.getByRole("region", {
    name: "Terminal session history",
    exact: true,
  });
  const rows = history
    .getByRole("row")
    .filter({ hasText: `${name} environment` });
  await expect(rows).toHaveCount(2);
  const own = rows.filter({ hasText: "Worker, instance 1" });
  await expect(own).toContainText(`${name} server`);
  await expect(own).toContainText("You");
  await expect(own).toContainText("Ended");
  await expect(own).toContainText("Under a minute");
  await expect(own).toContainText("Closed by the person who opened it");
  const other = rows.filter({ hasText: "Worker, instance 2" });
  await expect(other).toContainText("Sam Operator");
  await expect(other).toContainText("Ended by an owner");
  await new AxeBuilder({ page }).analyze().then(({ violations }) => {
    expect(violations).toEqual([]);
  });
});
