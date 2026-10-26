import { execFileSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import { expect } from "@playwright/test";

export async function resetLinkFixture() {
  const accountId = randomUUID();
  const email = `${accountId}@example.test`;
  const token = randomUUID();
  const hash = createHash("sha256").update(token).digest("hex");
  // Password-reset journeys must never change the shared fixture owner's account.
  await authFixtureSQL(
    `INSERT INTO accounts(id,email,name,password_hash) SELECT '${accountId}','${email}','Reset test',password_hash FROM accounts WHERE email='owner@example.test'; INSERT INTO password_resets(account_id,token_hash,expires_at) VALUES('${accountId}',decode('${hash}','hex'),now()+interval '30 minutes')`,
  );
  return { accountId, email, token };
}

export async function emailVerificationFixture() {
  const accountId = randomUUID();
  const email = `${accountId}@example.test`;
  const token = randomUUID();
  const hash = createHash("sha256").update(token).digest("hex");
  await authFixtureSQL(
    `INSERT INTO accounts(id,email,name,password_hash) SELECT '${accountId}','${email}','Email test',password_hash FROM accounts WHERE email='owner@example.test'; INSERT INTO email_verifications(account_id,token_hash,expires_at) VALUES('${accountId}',decode('${hash}','hex'),now()+interval '1 hour')`,
  );
  return { accountId, email, token };
}

export async function invitationLinkFixture(
  email = `${randomUUID()}@example.test`,
) {
  const workspaceId = randomUUID();
  const invitationId = randomUUID();
  const ownerId = randomUUID();
  const token = randomUUID();
  const hash = createHash("sha256").update(token).digest("hex");
  await authFixtureSQL(
    `INSERT INTO accounts(id,email,name,password_hash) SELECT '${ownerId}','${ownerId}@example.test','Inviting owner',password_hash FROM accounts WHERE email='owner@example.test'; INSERT INTO workspaces(id,name) VALUES('${workspaceId}','Invited workspace'); INSERT INTO memberships(workspace_id,account_id,role) VALUES('${workspaceId}','${ownerId}','owner'); INSERT INTO invitations(workspace_id,id,email,role,token_hash,expires_at) VALUES('${workspaceId}','${invitationId}','${email}','viewer',decode('${hash}','hex'),now()+interval '1 day')`,
  );
  return { workspaceId, invitationId, email, token };
}

export async function authFixtureSQL(statement: string) {
  const fixture: { host: string; container: string; owner: string } =
    JSON.parse(
      await readFile(
        new URL(
          "../../../.artifacts/p8/browser/hosted-fixture.json",
          import.meta.url,
        ),
        "utf8",
      ),
    );
  const ownership = execFileSync(
    "docker",
    [
      "--host",
      fixture.host,
      "inspect",
      "--format",
      '{{ index .Config.Labels "com.astilba.p1-test" }}',
      fixture.container,
    ],
    { encoding: "utf8" },
  ).trim();
  expect(fixture.host).toBe(process.env.PLAT_APP_DOCKER_HOST);
  expect(ownership).toBe(fixture.owner);
  execFileSync(
    "docker",
    [
      "--host",
      fixture.host,
      "exec",
      fixture.container,
      "psql",
      "--username",
      "postgres",
      "--dbname",
      "postgres",
      "-v",
      "ON_ERROR_STOP=1",
      "-c",
      statement,
    ],
    { stdio: "pipe" },
  );
}
