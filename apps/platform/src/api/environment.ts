import { queryOptions } from "@tanstack/react-query";
import { client, result } from "./client";

export type EnvironmentPath = {
  workspaceId: string;
  projectId: string;
  environmentId: string;
};
const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}";

export const draftOptions = (path: EnvironmentPath) =>
  queryOptions({
    queryKey: ["draft", path],
    queryFn: ({ signal }) =>
      result(client.GET(`${root}/draft`, { params: { path }, signal })),
  });
export const runtimeOptions = (path: EnvironmentPath) =>
  queryOptions({
    queryKey: ["runtime", path],
    queryFn: ({ signal }) =>
      result(client.GET(`${root}/runtime`, { params: { path }, signal })),
    refetchInterval: 5000,
  });
