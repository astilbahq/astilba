import { queryOptions } from "@tanstack/react-query";
import { client, result } from "./client";

export const membersOptions = (workspaceId: string) =>
  queryOptions({
    queryKey: ["members", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/members", {
          params: { path: { workspaceId } },
        }),
      ),
  });
