import { queryOptions } from "@tanstack/react-query";
import { client, result } from "./client";

export const environmentsOptions = (
  workspaceId: string,
  projectId: string,
  environmentId?: string,
) =>
  queryOptions({
    queryKey: [
      "environments",
      workspaceId,
      projectId,
      ...(environmentId ? [environmentId] : []),
    ],
    enabled: !!workspaceId && !!projectId,
    queryFn: ({ signal }) =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments",
          { params: { path: { workspaceId, projectId } }, signal },
        ),
      ),
  });
