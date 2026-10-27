import { queryOptions } from "@tanstack/react-query";
import { client, result } from "./client";

export const projectsOptions = (workspaceId: string) =>
  queryOptions({
    queryKey: ["projects", workspaceId],
    enabled: !!workspaceId,
    queryFn: ({ signal }) =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/projects", {
          params: { path: { workspaceId } },
          signal,
        }),
      ),
  });

export const projectOptions = (workspaceId: string, projectId: string) =>
  queryOptions({
    queryKey: ["project", workspaceId, projectId],
    enabled: !!workspaceId && !!projectId,
    queryFn: ({ signal }) =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/projects/{projectId}", {
          params: { path: { workspaceId, projectId } },
          signal,
        }),
      ),
  });
