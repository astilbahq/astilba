import { http, HttpResponse } from "msw/http";
import type { Schema } from "../api/client";
import { environments, session } from "./app-preview";
import { revision } from "./environment-fixtures";

export const workspaceRoot = "/api/v1/workspaces/:workspaceId";
const [api, worker] = revision.specification.applications;
const server = {
  id: "00000000-0000-4000-8000-000000000400",
  name: "app-server-with-a-long-descriptive-host-name-in-frankfurt",
} as Schema["Server"];
const otherPerson: Schema["Member"] = {
  id: "00000000-0000-4000-8000-000000000501",
  name: "Sam",
  email: "sam@example.test",
  role: "operator",
  mfaEnabled: true,
};
export const ownSession: Schema["TroubleshootingSession"] = {
  id: "00000000-0000-4000-8000-000000000601",
  environmentId: environments[0].id,
  serverId: server.id,
  applicationId: api.id,
  instance: 1,
  actorId: session.user!.id,
  state: "active",
  createdAt: "2026-11-04T10:00:00Z",
  deadlineAt: "2099-01-01T11:00:00Z",
};
export const otherSession: Schema["TroubleshootingSession"] = {
  ...ownSession,
  id: "00000000-0000-4000-8000-000000000602",
  applicationId: worker.id,
  instance: 2,
  actorId: otherPerson.id,
  state: "offered",
};
export const nameHandlers = [
  http.get(`${workspaceRoot}/servers`, () => HttpResponse.json([server])),
  http.get(`${workspaceRoot}/members`, () => HttpResponse.json([otherPerson])),
];
export const workshopFailure = (
  code: string,
  message: string,
  status: number,
) =>
  HttpResponse.json(
    { code, message, requestId: "workshop" } satisfies Schema["Error"],
    { status },
  );
