import { http, HttpResponse } from "msw/http";
import type { Schema } from "../api/client";

export const environmentRoot =
  "/api/v1/workspaces/:workspaceId/projects/:projectId/environments/:environmentId";
export const draftPath = `${environmentRoot}/draft`;
export const runtimePath = `${environmentRoot}/runtime`;
export const revision: Schema["Revision"] = {
  id: "00000000-0000-4000-8000-000000000100",
  authorId: "00000000-0000-4000-8000-000000000001",
  createdAt: "2026-01-01T12:00:00Z",
  hash: "8b3d2f7ea104ca8a13e587d1",
  specification: {
    version: 1,
    variables: [],
    applications: [
      {
        id: "00000000-0000-4000-8000-000000000300",
        name: "api",
        kind: "web",
        source: { kind: "image", image: "nginx:alpine" },
      },
      {
        id: "00000000-0000-4000-8000-000000000301",
        name: "worker",
        kind: "worker",
        source: { kind: "image", image: "redis:alpine" },
      },
    ],
  },
};
export const composeRevision: Schema["Revision"] = {
  ...revision,
  specification: {
    ...revision.specification,
    compose: {
      namespace: "development",
      entrypoints: ["compose.yaml"],
      files: [
        {
          path: "compose.yaml",
          content:
            "services:\n  api:\n    image: nginx:alpine\n  worker:\n    image: redis:alpine\n",
        },
      ],
    },
  },
};
export const observation: Schema["VisualObservation"] = {
  observedAt: "2026-01-01T12:10:00Z",
  applied: true,
  stateHash: "workshop-state",
  resources: revision.specification.applications.map((item) => ({
    id: item.id,
    kind: "application",
    state: "running",
    image: item.source.image!,
    drift: false,
    note: "",
    logs: "",
    memoryBytes: 0,
  })),
};
export const noObservation: Schema["VisualObservation"] = {
  observedAt: "0001-01-01T00:00:00Z",
  resources: [],
  applied: false,
  stateHash: "",
};
export const workshopError = (code = "not_found", status = 404) =>
  HttpResponse.json(
    { code, message: "Workshop request rejected", requestId: "workshop" },
    { status },
  );

export function environmentHandlers() {
  return [
    http.get(draftPath, ({ params }) =>
      HttpResponse.json(
        String(params.environmentId).endsWith("021")
          ? composeRevision
          : revision,
      ),
    ),
    http.put(draftPath, async ({ request }) => {
      const body = (await request.json()) as Schema["DraftUpdate"];
      return HttpResponse.json({
        ...revision,
        specification: body.specification,
      });
    }),
    http.get(runtimePath, () => HttpResponse.json(noObservation)),
    http.get(`${environmentRoot}/revisions`, () =>
      HttpResponse.json([revision]),
    ),
    http.get(`${environmentRoot}/revisions/:revisionId`, () =>
      HttpResponse.json(revision),
    ),
    http.get(`${environmentRoot}/layout`, () =>
      HttpResponse.json({ positions: {} } satisfies Schema["Layout"]),
    ),
    http.put(`${environmentRoot}/layout`, async ({ request }) =>
      HttpResponse.json(await request.json()),
    ),
    http.post(`${environmentRoot}/compose/preview`, () =>
      HttpResponse.json({
        specification: composeRevision.specification,
        diagnostics: [],
        permissions: [],
      }),
    ),
    ...[
      "secrets",
      "automation-credentials",
      "releases",
      "volumes",
      "builds",
      "upgrades",
      "backup-policies",
      "previews",
    ].map((suffix) =>
      http.get(`${environmentRoot}/${suffix}`, () => HttpResponse.json([])),
    ),
    ...["source", "preview-policy"].map((suffix) =>
      http.get(`${environmentRoot}/${suffix}`, () => workshopError()),
    ),
    http.get("/api/v1/workspaces/:workspaceId/github-app", () =>
      HttpResponse.json({
        configured: false,
      } satisfies Schema["GitHubAppStatus"]),
    ),
    ...[
      "servers",
      "operations",
      "git-connections",
      "registries",
      "build-secrets",
      "artifacts",
      "backup-destinations",
      "service-catalog",
    ].map((suffix) =>
      http.get(`/api/v1/workspaces/:workspaceId/${suffix}`, () =>
        HttpResponse.json([]),
      ),
    ),
    http.get(
      `${environmentRoot}/events`,
      () =>
        new HttpResponse("retry: 3600000\n\n", {
          headers: { "Content-Type": "text/event-stream" },
        }),
    ),
  ];
}
