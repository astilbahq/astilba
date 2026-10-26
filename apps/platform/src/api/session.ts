import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { ApiError, loadSession } from "./client";

export const sessionOptions = queryOptions({
  queryKey: ["session"],
  queryFn: loadSession,
});

export async function refreshRejectedSession(
  error: unknown,
  queries: QueryClient,
) {
  if (error instanceof ApiError && [401, 403].includes(error.status)) {
    await queries.invalidateQueries({ queryKey: sessionOptions.queryKey });
  }
}
