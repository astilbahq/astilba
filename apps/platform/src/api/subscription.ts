import { queryOptions, skipToken } from "@tanstack/react-query";
import { client, result } from "./client";
import { refreshRejectedSession } from "./session";

export function subscriptionOptions(workspaceId?: string) {
  return queryOptions({
    queryKey: ["subscription", workspaceId],
    queryFn: workspaceId
      ? async ({ signal, client: queries }) => {
          try {
            return await result(
              client.GET("/api/v1/workspaces/{workspaceId}/subscription", {
                params: { path: { workspaceId } },
                signal,
              }),
            );
          } catch (error) {
            await refreshRejectedSession(error, queries);
            throw error;
          }
        }
      : skipToken,
    retry: false,
  });
}
