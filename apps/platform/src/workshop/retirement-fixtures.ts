import { http, HttpResponse } from "msw/http";
import type { Schema } from "../api/client";
import { environments, environmentsPath } from "./app-preview";
import { environmentRoot } from "./environment-fixtures";

// A retired copy of the first workshop environment, and the handlers its banner reads.
export const ownerId = "00000000-0000-4000-8000-000000000050";
export const operationId = "00000000-0000-4000-8000-000000000060";
export const retired: Schema["Environment"] = {
  ...environments[0],
  state: "retired",
  retiredAt: "2026-11-10T09:30:00Z",
  retiredBy: ownerId,
  retirementOperationId: operationId,
};
export const retiring: Schema["Environment"] = {
  ...retired,
  state: "retiring",
};
export const listed = (environment: Schema["Environment"]) => [
  environment,
  ...environments.slice(1),
];
export const listing = (environment: Schema["Environment"]) =>
  http.get(environmentsPath, () => HttpResponse.json(listed(environment)));
export const members = http.get("/api/v1/workspaces/:workspaceId/members", () =>
  HttpResponse.json([
    {
      id: ownerId,
      name: "Ada Lovelace",
      email: "ada@example.com",
      role: "owner",
      mfaEnabled: true,
    },
  ] satisfies Schema["Member"][]),
);
export const keptVolumes: Schema["ManagedVolumes"] = [
  {
    id: "00000000-0000-4000-8000-000000000070",
    name: "postgres-data",
    inUse: false,
    deleted: false,
  },
  {
    id: "00000000-0000-4000-8000-000000000071",
    name: "uploads",
    inUse: false,
    deleted: false,
  },
];
export const volumes = http.get(`${environmentRoot}/volumes`, () =>
  HttpResponse.json(keptVolumes),
);
export const operationFixture = (
  state: Schema["ExecutionState"],
  note = "",
  id = operationId,
): Schema["Operation"] => ({
  id,
  serverId: "00000000-0000-4000-8000-000000000080",
  environmentId: environments[0].id,
  generation: 1,
  state,
  kind: "update",
  cancelRequested: false,
  note,
  steps: [],
  createdAt: "2026-11-10T09:30:00Z",
});
export const operation = (
  state: Schema["ExecutionState"],
  note: string,
  id = operationId,
) =>
  http.get(`${environmentRoot}/operations/:operationId`, () =>
    HttpResponse.json(operationFixture(state, note, id)),
  );

// The retiring and purging pages poll every five seconds.
export const pollWait = 7000;
