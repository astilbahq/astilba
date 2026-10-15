import assert from "node:assert/strict";
import { realpathSync } from "node:fs";
import { readFile } from "node:fs/promises";

import { requireActivation } from "./activation.mjs";

const workers = new Set([
  "astilba-site",
  "astilba-docs",
  "astilba-ui-showcase",
]);
const uuid = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/u;
const accountId = /^[0-9a-f]{32}$/u;

// One Worker only. Approval, writer exclusion and smoke checks belong to the runbook.
export const rollbackVersion = async (
  { account, worker, version, expectedDeployment, token },
  request = fetch
) => {
  assert.ok(workers.has(worker), "Unknown production Worker");
  assert.match(account, accountId);
  assert.match(version, uuid);
  assert.match(expectedDeployment, uuid);
  assert.ok(typeof token === "string" && token.length > 0, "Missing API token");
  const base = `https://api.cloudflare.com/client/v4/accounts/${account}/workers/scripts/${worker}`;
  const api = async (path, body) => {
    const response = await request(`${base}/${path}`, {
      body: body === undefined ? undefined : JSON.stringify(body),
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      method: body === undefined ? "GET" : "POST",
      redirect: "error",
      signal: AbortSignal.timeout(30_000),
    });
    assert.ok(
      response.ok,
      "Cloudflare request failed; inspect provider state before retrying"
    );
    const envelope = await response.json();
    assert.equal(
      envelope.success,
      true,
      "Cloudflare rejected request; no automatic retry"
    );
    return envelope.result;
  };
  const target = await api(`versions/${version}`);
  assert.equal(target.id, version, "Unexpected rollback version");
  const currentState = await api("deployments");
  const [current] = currentState.deployments;
  assert.equal(
    current?.id,
    expectedDeployment,
    "Deployment changed; stop and reconcile"
  );
  assert.equal(
    current.versions.length,
    1,
    "Split deployments need separate review"
  );
  assert.equal(current.versions[0].percentage, 100);

  // No force query, retry, implicit previous version, upload or resource mutation.
  const restored = await api("deployments", {
    annotations: { "workers/message": "Astilba cutover rollback" },
    strategy: "percentage",
    versions: [{ percentage: 100, version_id: version }],
  });
  assert.match(restored.id, uuid);
  const restoredState = await api("deployments");
  const [active] = restoredState.deployments;
  assert.equal(
    active?.id,
    restored.id,
    "Rollback readback differs; stop and reconcile"
  );
  assert.deepEqual(active.versions, [{ percentage: 100, version_id: version }]);
  return restored.id;
};

if (
  process.argv[1] !== undefined &&
  realpathSync(process.argv[1]) === import.meta.filename
) {
  requireActivation(
    JSON.parse(
      await readFile(new URL("cutover.json", import.meta.url), "utf-8")
    )
  );
  assert.equal(
    process.argv.length,
    5,
    "Expected Worker, baseline version and current deployment IDs"
  );
  await rollbackVersion({
    account: process.env.CLOUDFLARE_ACCOUNT_ID,
    expectedDeployment: process.argv[4],
    token: process.env.CLOUDFLARE_API_TOKEN,
    version: process.argv[3],
    worker: process.argv[2],
  });
  process.stdout.write(
    "Version restored; run the required public smoke checks.\n"
  );
}
