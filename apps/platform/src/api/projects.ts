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
