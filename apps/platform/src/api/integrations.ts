import { queryOptions } from "@tanstack/react-query";
import { client, result } from "./client";

export const gitConnectionsOptions = (workspaceId: string) =>
  queryOptions({
    queryKey: ["git-connections", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/git-connections", {
          params: { path: { workspaceId } },
        }),
      ),
  });
