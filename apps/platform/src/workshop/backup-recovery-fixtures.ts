import { http, HttpResponse } from "msw/http";
import type { Schema } from "../api/client";
import { environmentRoot, revision } from "./environment-fixtures";

// A policy whose server was revoked, after its environment moved to a replacement server, with
// a destination the revoked server held the keys of.
const revoked = "00000000-0000-4000-8000-000000000a01";
export const replacement = "00000000-0000-4000-8000-000000000a02";
const destination = "00000000-0000-4000-8000-000000000a03";
const policyId = "00000000-0000-4000-8000-000000000a04";
const resource = "00000000-0000-4000-8000-000000000a05";
const environment = "00000000-0000-4000-8000-000000000a06";
const workspace = "00000000-0000-4000-8000-000000000a07";
const newer = "4f1c".padEnd(64, "a");
const older = "9b2e".padEnd(64, "b");

const server = (
  id: string,
  name: string,
  status: Schema["Server"]["status"],
): Schema["Server"] => ({
  id,
  name,
  role: "combined",
  status,
  lastSeen: "2026-03-02T08:10:00Z",
  capabilities: null,
  preflight: null,
  rotationRequested: false,
  ...(status === "revoked" ? { revokedAt: "2026-03-02T09:00:00Z" } : {}),
});

const plan: Schema["VisualPlan"] = {
  id: "00000000-0000-4000-8000-000000000a08",
  serverId: revoked,
  environmentId: environment,
  unitId: "00000000-0000-4000-8000-000000000a09",
  revisionId: revision.id,
  expectedAppliedId: null,
  recipe: 4,
  specification: revision.specification,
  images: {},
  hostChanges: false,
  effects: [],
  hash: revision.hash,
  expiresAt: "2026-03-01T00:00:00Z",
  expectedState: "",
  acceptDrift: false,
};

const policy: Schema["BackupPolicy"] = {
  id: policyId,
  name: "Database daily backup",
  workspaceId: workspace,
  environmentId: environment,
  serverId: revoked,
  destinationId: destination,
  resourceId: resource,
  generation: 3,
  method: "postgres-logical",
  schedule: { hour: 2, minute: 30, timezone: "UTC" },
  retain: 7,
  enabled: true,
  plan,
};

const manifest = (
  snapshot: string,
  createdAt: string,
  bytes: number,
): Schema["BackupManifest"] => ({
  recipe: 1,
  policyId,
  runId: `00000000-0000-4000-8000-0000000000${snapshot.slice(0, 2)}`,
  environmentId: environment,
  resourceId: resource,
  method: "postgres-logical",
  image: "postgres:18.4-alpine3.23",
  database: "app",
  username: "app",
  createdAt,
  roles: [],
  extensions: [],
  sha256: snapshot,
  bytes,
});

const run = (
  snapshot: string,
  at: string,
  bytes: number,
): Schema["BackupRun"] => ({
  id: `00000000-0000-4000-8000-0000000001${snapshot.slice(0, 2)}`,
  policyId,
  generation: 3,
  scheduledAt: at,
  startedAt: at,
  finishedAt: at,
  state: "succeeded",
  note: "Uploaded.",
  snapshot,
  verified: false,
  protected: false,
  pruned: false,
  exitCode: null,
  manifest: manifest(snapshot, at, bytes),
  targetId: "00000000-0000-0000-0000-000000000000",
});

const recovery = (
  id: string,
  snapshot: string,
  firstReportedAt: string,
  state: Schema["ExecutionState"],
  note: string,
): Schema["BackupRecoveryRecord"] => ({
  id,
  operationId: id.replace("0b", "0c"),
  sourceServerId: revoked,
  serverId: replacement,
  planId: plan.id,
  snapshot,
  firstReportedAt,
  targetId: id.replace("0b", "0d"),
  createdAt: "2026-03-02T10:05:00Z",
  state,
  note,
  restored:
    state === "succeeded"
      ? { manifest: manifest(snapshot, "2026-03-01T02:30:00Z", 48_200_000) }
      : null,
  targetDeleted: false,
  discard: null,
});

// A discard stays running, so the row shows it pending, as after a reload. Stories share
// handlers, so each story starts with none.
const discards = new Map<string, Schema["BackupRecoveryRecord"]["discard"]>();
let reloadsToFail = 0;
export function resetRevokedServerBackups() {
  discards.clear();
  reloadsToFail = 0;
}

/**
 * With `refuseDiscard`, another owner's discard starts first, so the owner's own is refused.
 * With `failReload`, the reload after an accepted discard fails, and by the next one the
 * discard has already failed. With `loseReply`, the discard starts but its reply is lost.
 */
const placeholderDestination: Schema["BackupDestination"] = {
  id: "00000000-0000-4000-8000-000000000a10",
  name: "Managed bucket",
  kind: "s3",
  root: "",
  endpoint: "https://s3.eu-west-2.amazonaws.com",
  bucket: "managed-backups",
  region: "eu-west-2",
  serverId: replacement,
  shared: false,
  exposed: false,
  accountId: "00000000-0000-4000-8000-000000000a11",
  keys: [
    {
      id: "00000000-0000-4000-8000-000000000a12",
      purpose: "server",
      state: "minting",
      userName: "astilba-00000000-0000-4000-8000-000000000a12",
      serverId: replacement,
      handedOver: false,
      activatedAt: null,
      revokedAt: null,
      attempts: 0,
      lastError: null,
    },
  ],
  remintError: null,
  policies: [],
};

export function revokedServerBackupHandlers({
  refuseDiscard = false,
  failReload = false,
  loseReply = false,
  // Adds a managed destination for the replacement server whose key management is still making.
  placeholder = false,
} = {}) {
  const policyPath = `${environmentRoot}/backup-policies/:policyId`;
  const withDiscard = (r: Schema["BackupRecoveryRecord"]) => ({
    ...r,
    discard: discards.get(r.id) ?? null,
  });
  return [
    http.get("/api/v1/workspaces/:workspaceId/servers", () =>
      HttpResponse.json([
        server(revoked, "edge-1", "revoked"),
        server(replacement, "edge-2", "online"),
      ]),
    ),
    http.get("/api/v1/workspaces/:workspaceId/backup-destinations", () =>
      HttpResponse.json([
        {
          id: destination,
          name: "Offsite bucket",
          kind: "s3",
          root: "",
          endpoint: "https://storage.example.test",
          bucket: "backups",
          region: "eu-west-1",
          serverId: revoked,
          shared: false,
          exposed: true,
          accountId: null,
          keys: [],
          remintError: null,
          policies: [
            {
              id: policyId,
              name: policy.name,
              projectId: "00000000-0000-4000-8000-000000000a0a",
              environmentId: environment,
              environmentName: "Production",
            },
          ],
        },
        ...(placeholder ? [placeholderDestination] : []),
      ] satisfies Schema["BackupDestination"][]),
    ),
    http.get(`${environmentRoot}/backup-policies`, () =>
      HttpResponse.json([policy]),
    ),
    http.get(`${policyPath}/status`, () =>
      HttpResponse.json({
        enabled: true,
        policyId,
        generation: 3,
        next: "2026-03-03T02:30:00Z",
        runs: [
          run(older, "2026-02-28T02:30:00Z", 47_900_000),
          run(newer, "2026-03-01T02:30:00Z", 48_200_000),
        ],
      } satisfies Schema["BackupStatus"]),
    ),
    http.get(`${policyPath}/receipts`, () =>
      HttpResponse.json([
        { snapshot: newer, firstReportedAt: "2026-03-01T02:31:04Z" },
        { snapshot: older, firstReportedAt: "2026-02-28T02:31:10Z" },
      ] satisfies Schema["BackupSnapshotReceipt"][]),
    ),
    http.delete(`${policyPath}/recoveries/:recoveryId/target`, ({ params }) => {
      const operation = {
        id: "00000000-0000-4000-8000-00000000c0e1",
        serverId: replacement,
        environmentId: environment,
        generation: 9,
        state: "queued",
        kind: "visual",
        cancelRequested: false,
        note: "",
        steps: [],
        createdAt: "2026-03-02T11:00:00Z",
      } satisfies Schema["Operation"];
      discards.set(
        String(params.recoveryId),
        failReload
          ? {
              operationId: operation.id,
              state: "failed",
              note: "The server couldn't delete the volume.",
            }
          : { operationId: operation.id, state: "running", note: "" },
      );
      if (failReload) reloadsToFail = 1;
      if (loseReply)
        return HttpResponse.json(
          {
            code: "unavailable",
            message: "The platform couldn't be reached.",
            requestId: "workshop",
          } satisfies Schema["Error"],
          { status: 502 },
        );
      if (refuseDiscard)
        return HttpResponse.json(
          {
            code: "conflict",
            message: "Server text the dashboard does not show.",
            requestId: "workshop",
            fields: ["discard.active"],
          } satisfies Schema["Error"],
          { status: 409 },
        );
      return HttpResponse.json(operation, { status: 201 });
    }),
    http.get(`${policyPath}/recoveries`, () => {
      if (reloadsToFail > 0) {
        reloadsToFail--;
        return HttpResponse.json(
          {
            code: "unavailable",
            message: "Server text the dashboard does not show.",
            requestId: "workshop",
          } satisfies Schema["Error"],
          { status: 503 },
        );
      }
      return HttpResponse.json(
        [
          recovery(
            "00000000-0000-4000-8000-00000000b0b1",
            newer,
            "2026-03-01T02:31:04Z",
            "succeeded",
            "Snapshot restored into a new volume and matched against its manifest.",
          ),
          recovery(
            "00000000-0000-4000-8000-00000000b0b2",
            older,
            "2026-02-28T02:31:10Z",
            "failed",
            "The restore failed while restoring or verifying its data.",
          ),
        ].map(withDiscard) satisfies Schema["BackupRecoveryRecord"][],
      );
    }),
  ];
}
