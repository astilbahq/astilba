import { http, HttpResponse } from "msw/http";
import type { Schema } from "../api/client";
import { workshopFailure, workspaceRoot } from "./terminal-session-fixtures";

// A workspace with one AWS storage account, whose latest check failed after finding users
// management didn't make, one of which management can't delete, and a managed destination with a
// key in each state.
const id = (n: number) =>
  `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
export const installation = id(0xb01);
export const otherInstallation = "7c1e4a52-9d3b-4f6a-8e21-5b0c9d7f3a18";
const peerInstallation = "2f8d6b14-3a7c-4e95-b0d2-6c1f8e4a9b37";
export const accountId = id(0xb02);
const destinationId = id(0xb03);
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
  checkedAt: "2026-11-20T03:00:00Z",
  checking: false,
  checkError: "access_denied",
  foundUsers: [
    {
      userName: "astilba-6f2c0d1e-91a4-4b7e-a5c3-0e8d2f7b1c49",
      installationId: otherInstallation,
      serverId: id(0xc01),
      bounded: true,
      peer: false,
      revoking: false,
      lastError: null,
    },
    {
      userName: "astilba-3b9e7a21-5c48-4d0f-b6e2-9a1c7f3d8e05",
      installationId: peerInstallation,
      serverId: id(0xc02),
      bounded: true,
      peer: true,
      revoking: false,
      lastError: null,
    },
    {
      userName: "backup-writer",
      installationId: "",
      serverId: "",
      bounded: false,
      peer: false,
      revoking: false,
      lastError: null,
    },
    {
      // Management is revoking it, and AWS won't delete it while something else is attached.
      userName: "astilba-8d4a2c17-6e0b-4f93-a1d5-3c7e9b2f6a80",
      installationId: installation,
      serverId: retired,
      bounded: true,
      peer: false,
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

const keys = (revoked: ReadonlySet<string>): Schema["BackupStorageKey"][] =>
  [
    key(0xb07, {}),
    key(0xb06, { serverId: retired, handedOver: true }),
    key(0xb08, {
      serverId: retired,
      state: "revoking",
      attempts: 4,
      lastError: "access_denied",
    }),
    key(0xb09, {
      state: "revoked",
      lastError: "superseded",
      activatedAt: "2026-10-01T09:00:00Z",
      revokedAt: "2026-11-01T09:20:00Z",
    }),
  ].map((k) =>
    revoked.has(k.id) ? { ...k, state: "revoking", attempts: 0 } : k,
  );

const destination = (
  revoked: ReadonlySet<string>,
): Schema["BackupDestination"] => ({
  id: destinationId,
  name: "Edge backups",
  kind: "s3",
  root: "",
  endpoint: "https://s3.eu-west-2.amazonaws.com",
  bucket: "acme-backups",
  region: "eu-west-2",
  prefix: "servers/edge-1",
  serverId: edge,
  shared: false,
  exposed: true,
  accountId,
  keys: keys(revoked),
  remintError: "timeout",
  policies: [],
});

// Keys and users an owner revoked in a story. Stories share handlers, so each starts with none.
const revokedKeys = new Set<string>();
const addedPeers = new Set<string>();
const revokedUsers = new Set<string>();
// The body of the latest admin key replacement, which names no boundary.
let replacement: Record<string, unknown> | undefined;
export const lastReplacement = () => replacement;
export function resetBackupStorage() {
  revokedKeys.clear();
  addedPeers.clear();
  revokedUsers.clear();
  replacement = undefined;
}

export function backupStorageHandlers({
  accounts = [account],
  failAccounts = false,
  failDestinations = false,
  // Revoking a found user is refused: its installation became a peer since the latest check.
  refusePeerRevoke = false,
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
              peers: [...a.peers, ...addedPeers],
              foundUsers: a.foundUsers.map((u) =>
                revokedUsers.has(u.userName) ? { ...u, revoking: true } : u,
              ),
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
        : HttpResponse.json([destination(revokedKeys)]),
    ),
    http.get(`${workspaceRoot}/servers`, () =>
      HttpResponse.json([
        server(edge, "edge-1", "online"),
        server(retired, "edge-0", "revoked"),
      ]),
    ),
    http.post(`${destinationPath}/keys/:keyId/revoke`, ({ params }) => {
      revokedKeys.add(String(params.keyId));
      return new HttpResponse(null, { status: 204 });
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
      if (refusePeerRevoke)
        return workshopFailure(
          "conflict",
          "This user belongs to a peer installation, which may still use its key. Revoke it from that installation.",
          409,
        );
      revokedUsers.add(String(params.userName));
      return new HttpResponse(null, { status: 204 });
    }),
    http.post(`${workspaceRoot}/backup-storage-accounts`, () =>
      workshopFailure(
        "invalid_input",
        "AWS did not accept this access key.",
        400,
      ),
    ),
  ];
}
