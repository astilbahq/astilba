import { queryOptions } from "@tanstack/react-query";
import { client, result } from "./client";

export const serversOptions = (workspaceId: string) =>
  queryOptions({
    queryKey: ["servers", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/servers", {
          params: { path: { workspaceId } },
        }),
      ),
  });
