import { http, HttpResponse } from "msw/http";
import type { Schema } from "../api/client";
import { workshopFailure, workspaceRoot } from "./terminal-session-fixtures";

// A workspace with one AWS storage account, whose latest check failed after finding users
// management didn't make, one of which management can't delete, a managed destination on a live
// server whose rotation keeps failing, and one on a revoked server whose keys are handed over or
// being revoked.
const id = (n: number) =>
  `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
export const installation = id(0xb01);
export const otherInstallation = "7c1e4a52-9d3b-4f6a-8e21-5b0c9d7f3a18";
const peerInstallation = "2f8d6b14-3a7c-4e95-b0d2-6c1f8e4a9b37";
export const accountId = id(0xb02);
const destinationId = id(0xb03);
const retiredDestinationId = id(0xb0a);
const edge = id(0xb04);
const retired = id(0xb05);
export const handedOverKey = id(0xb06);
export const currentKey = id(0xb07);

const server = (
  serverId: string,
  name: string,
  status: Schema["Server"]["status"],
): Schema["Server"] => ({
  id: serverId,
  name,
  role: "combined",
  status,
  lastSeen: "2026-11-20T08:10:00Z",
  capabilities: null,
  preflight: null,
  rotationRequested: false,
});

export const account: Schema["BackupStorageAccount"] = {
  id: accountId,
  name: "Production AWS",
  awsAccount: "123456789012",
  adminArn: "arn:aws:iam::123456789012:user/astilba-admin",
  boundary: "arn:aws:iam::123456789012:policy/astilba-backups",
  path: "/astilba/00000000-0000-4000-8000-000000000001/",
  installationId: installation,
  generation: 2,
  peers: [peerInstallation],
  keyRotationDays: 90,
  checkedAt: "2026-11-20T03:00:00Z",
  checking: false,
  checkError: "access_denied",
  foundUsers: [
    {
      id: id(0xd01),
      userName: "astilba-6f2c0d1e-91a4-4b7e-a5c3-0e8d2f7b1c49",
      userId: "AIDAEXAMPLE000000001",
      accessKeyIds: ["AKIAEXAMPLE000000001"],
      installationId: otherInstallation,
      serverId: id(0xc01),
      bounded: true,
      peer: false,
      acknowledgedAt: null,
      revoking: false,
      lastError: null,
    },
    {
      // A peer's user the owner hasn't acknowledged yet, which raises an alert.
      id: id(0xd02),
      userName: "astilba-3b9e7a21-5c48-4d0f-b6e2-9a1c7f3d8e05",
      userId: "AIDAEXAMPLE000000002",
      accessKeyIds: ["AKIAEXAMPLE000000002"],
      installationId: peerInstallation,
      serverId: id(0xc02),
      bounded: true,
      peer: true,
      acknowledgedAt: null,
      revoking: false,
      lastError: null,
    },
    {
      id: id(0xd03),
      userName: "astilba-1a7f3e90-2b6c-4d18-9e05-7c3b8a1f4d62",
      userId: "AIDAEXAMPLE000000003",
      accessKeyIds: ["AKIAEXAMPLE000000003"],
      installationId: peerInstallation,
      serverId: id(0xc03),
      bounded: true,
      peer: true,
      acknowledgedAt: "2026-11-18T10:00:00Z",
      revoking: false,
      lastError: null,
    },
    {
      id: id(0xd04),
      userName: "backup-writer",
      userId: "AIDAEXAMPLE000000004",
      accessKeyIds: [],
      installationId: "",
      serverId: "",
      bounded: false,
      peer: false,
      acknowledgedAt: null,
      revoking: false,
      lastError: null,
    },
    {
      // Management is revoking it, and AWS won't delete it while something else is attached.
      id: id(0xd05),
      userName: "astilba-8d4a2c17-6e0b-4f93-a1d5-3c7e9b2f6a80",
      userId: "AIDAEXAMPLE000000005",
      accessKeyIds: [],
      installationId: installation,
      serverId: retired,
      bounded: true,
      peer: false,
      acknowledgedAt: null,
      revoking: true,
      lastError: "delete_conflict",
    },
  ],
  extraKeysDeleted: 1,
};

const key = (
  n: number,
  value: Partial<Schema["BackupStorageKey"]>,
): Schema["BackupStorageKey"] => ({
  id: id(n),
  purpose: "server",
  state: "active",
  userName: `astilba-${id(n)}`,
  serverId: edge,
  handedOver: false,
  activatedAt: "2026-11-01T09:00:00Z",
  revokedAt: null,
  attempts: 0,
  lastError: null,
  ...value,
});

const revokedBy =
  (revoked: ReadonlySet<string>) =>
  (k: Schema["BackupStorageKey"]): Schema["BackupStorageKey"] =>
    revoked.has(k.id) ? { ...k, state: "revoking", attempts: 0 } : k;

const managed = (
  value: Pick<
    Schema["BackupDestination"],
    "id" | "name" | "serverId" | "exposed" | "keys" | "remintError"
  > &
    Partial<Schema["BackupDestination"]>,
): Schema["BackupDestination"] => ({
  kind: "s3",
  root: "",
  endpoint: "https://s3.eu-west-2.amazonaws.com",
  bucket: "acme-backups",
  region: "eu-west-2",
  shared: false,
  accountId,
  placeholder: false,
  rotationRequested: false,
  rotationDueAt: null,
  policies: [],
  ...value,
});

// When the edge server's current key was activated. Its 90 days have passed, so it is due.
const currentActivated = "2026-08-20T09:00:00Z";

// The live server's destination: its current key, the one it superseded, and the rotation that is
// due, whose new key keeps failing to be made. As management does, the due date follows the
// account's key rotation, and once the owner asks for a rotation it waits for its key. A revoked
// server's key isn't replaced, so it has no due date.
const destinations = (
  revoked: ReadonlySet<string>,
  rotated: ReadonlySet<string>,
  days: number | null,
  serverRevoked: boolean,
): Schema["BackupDestination"][] => [
  managed({
    id: destinationId,
    name: "Edge backups",
    prefix: "servers/edge-1",
    serverId: edge,
    exposed: false,
    keys: [
      key(0xb07, { activatedAt: currentActivated }),
      key(0xb09, {
        state: "revoked",
        lastError: "superseded",
        activatedAt: "2026-05-01T09:00:00Z",
        revokedAt: "2026-08-20T09:20:00Z",
      }),
    ].map(revokedBy(revoked)),
    remintError: "timeout",
    rotationRequested: rotated.has(destinationId),
    rotationDueAt:
      revoked.has(currentKey) ||
      rotated.has(destinationId) ||
      days === null ||
      serverRevoked
        ? null
        : new Date(
            Date.parse(currentActivated) + days * 24 * 60 * 60 * 1000,
          ).toISOString(),
  }),
  managed({
    id: retiredDestinationId,
    name: "Old edge backups",
    prefix: "servers/edge-0",
    serverId: retired,
    exposed: true,
    keys: [
      key(0xb06, { serverId: retired, handedOver: true }),
      key(0xb08, {
        serverId: retired,
        state: "revoking",
        attempts: 4,
        lastError: "access_denied",
      }),
    ].map(revokedBy(revoked)),
    remintError: null,
  }),
];

// Keys and users an owner revoked in a story. Stories share handlers, so each starts with none.
const revokedKeys = new Set<string>();
const addedPeers = new Set<string>();
const revokedUsers = new Set<string>();
// The destinations an owner asked to rotate, and the account's key rotation as last saved.
const rotatedDestinations = new Set<string>();
let rotation: { days: number | null } | undefined;
export const savedRotation = () => rotation;
// The body of the latest account an owner tried to add.
let creation: Schema["BackupStorageAccountRequest"] | undefined;
export const lastCreation = () => creation;
// The bodies of the acknowledgements an owner made in a story, by user name.
const acknowledged = new Map<
  string,
  Schema["BackupStorageAcknowledgementRequest"]
>();
export const acknowledgement = (userName: string) => acknowledged.get(userName);
// The body of the latest admin key replacement, which names no boundary.
let replacement: Record<string, unknown> | undefined;
export const lastReplacement = () => replacement;
// The servers list, which a story can hold until it has looked at the page without it.
let releaseServers = () => {};
let serversReleased = Promise.resolve();
export const releaseHeldServers = () => releaseServers();
// The live server, once a story has had it revoked since the page loaded.
let liveServerRevoked = false;
export function resetBackupStorage() {
  // A held list from the story before answers, so its request doesn't outlive it.
  releaseServers();
  serversReleased = new Promise((resolve) => {
    releaseServers = resolve;
  });
  liveServerRevoked = false;
  revokedKeys.clear();
  addedPeers.clear();
  revokedUsers.clear();
  acknowledged.clear();
  rotatedDestinations.clear();
  rotation = undefined;
  creation = undefined;
  replacement = undefined;
}

export function backupStorageHandlers({
  accounts = [account],
  failAccounts = false,
  failDestinations = false,
  // Revoking a found user is refused: another owner acknowledged it as a peer's since the page
  // loaded, which the refreshed list then shows.
  refusePeerRevoke = false,
  // The servers list answers only once the story calls releaseHeldServers.
  holdServers = false,
  // Rotating the live server's key is refused: the server was revoked since the page loaded, which
  // the refreshed servers list then shows.
  revokeOnRotate = false,
} = {}) {
  const accountPath = `${workspaceRoot}/backup-storage-accounts/:accountId`;
  const destinationPath = `${workspaceRoot}/backup-destinations/:destinationId`;
  return [
    http.get(`${workspaceRoot}/backup-storage-accounts`, () =>
      failAccounts
        ? workshopFailure(
            "internal",
            "Server text the dashboard does not show.",
            500,
          )
        : HttpResponse.json(
            accounts.map((a) => ({
              ...a,
              keyRotationDays: rotation ? rotation.days : a.keyRotationDays,
              peers: [...a.peers, ...addedPeers],
              // As management does, a found user is a peer's as soon as the peer is added.
              foundUsers: a.foundUsers.map((u) => ({
                ...u,
                peer: u.peer || addedPeers.has(u.installationId),
                revoking: u.revoking || revokedUsers.has(u.userName),
                acknowledgedAt: acknowledged.has(u.userName)
                  ? "2026-11-21T09:00:00Z"
                  : u.acknowledgedAt,
              })),
            })),
          ),
    ),
    http.get(`${workspaceRoot}/backup-destinations`, () =>
      failDestinations
        ? workshopFailure(
            "internal",
            "Server text the dashboard does not show.",
            500,
          )
        : HttpResponse.json(
            destinations(
              revokedKeys,
              rotatedDestinations,
              rotation ? rotation.days : account.keyRotationDays,
              liveServerRevoked,
            ),
          ),
    ),
    http.get(`${workspaceRoot}/servers`, async () => {
      if (holdServers) await serversReleased;
      return HttpResponse.json([
        server(edge, "edge-1", liveServerRevoked ? "revoked" : "online"),
        server(retired, "edge-0", "revoked"),
      ]);
    }),
    http.post(`${destinationPath}/keys/:keyId/revoke`, ({ params }) => {
      revokedKeys.add(String(params.keyId));
      return new HttpResponse(null, { status: 204 });
    }),
    http.post(`${destinationPath}/keys/rotate`, ({ params }) => {
      if (revokeOnRotate) {
        liveServerRevoked = true;
        return workshopFailure(
          "conflict",
          "This destination's server is revoked, so its keys aren't replaced.",
          409,
        );
      }
      rotatedDestinations.add(String(params.destinationId));
      return new HttpResponse(null, { status: 204 });
    }),
    http.put(`${accountPath}/key-rotation`, async ({ request }) => {
      const body =
        (await request.json()) as Schema["BackupStorageKeyRotationRequest"];
      rotation = { days: body.keyRotationDays };
      return HttpResponse.json({
        ...account,
        keyRotationDays: body.keyRotationDays,
      });
    }),
    http.post(
      `${destinationPath}/retry`,
      () => new HttpResponse(null, { status: 204 }),
    ),
    http.post(
      `${accountPath}/check`,
      () => new HttpResponse(null, { status: 204 }),
    ),
    http.put(`${accountPath}/credential`, async ({ request }) => {
      replacement = (await request.json()) as Record<string, unknown>;
      return HttpResponse.json({
        ...account,
        generation: account.generation + 1,
      });
    }),
    http.post(`${accountPath}/peers`, async ({ request }) => {
      const body =
        (await request.json()) as Schema["BackupStorageAccountPeerRequest"];
      addedPeers.add(body.installationId);
      return new HttpResponse(null, { status: 204 });
    }),
    http.post(`${accountPath}/found-users/:userName/revoke`, ({ params }) => {
      if (refusePeerRevoke) {
        acknowledged.set(String(params.userName), {
          userId: "",
          accessKeyIds: [],
        });
        return workshopFailure(
          "conflict",
          "This user is acknowledged as a peer installation's, which may still use its key. Revoke it from that installation.",
          409,
        );
      }
      revokedUsers.add(String(params.userName));
      return new HttpResponse(null, { status: 204 });
    }),
    http.post(
      `${accountPath}/found-users/:userName/acknowledge`,
      async ({ params, request }) => {
        acknowledged.set(
          String(params.userName),
          (await request.json()) as Schema["BackupStorageAcknowledgementRequest"],
        );
        return new HttpResponse(null, { status: 204 });
      },
    ),
    http.post(
      `${workspaceRoot}/backup-storage-accounts`,
      async ({ request }) => {
        creation =
          (await request.json()) as Schema["BackupStorageAccountRequest"];
        return workshopFailure(
          "invalid_input",
          "AWS did not accept this access key.",
          400,
        );
      },
    ),
  ];
}
