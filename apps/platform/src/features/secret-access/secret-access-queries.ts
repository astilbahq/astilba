import { infiniteQueryOptions } from "@tanstack/react-query";
import { client, result } from "../../api/client";

// Pages hold up to 100 records, newest first. A page's `next` is the ID to pass
// as `before` for the page after it, and is absent on the last page.
export const secretAccessOptions = (workspaceId: string) =>
  infiniteQueryOptions({
    queryKey: ["secret-access", workspaceId],
    queryFn: ({ pageParam, signal }) =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/secret-access", {
          params: {
            path: { workspaceId },
            query: pageParam ? { before: pageParam } : {},
          },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next,
  });
