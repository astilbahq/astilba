import { queryOptions } from "@tanstack/react-query";
import { client, result } from "./client";

// The account's signed-in sessions. The key names the account, so a cached list never shows
// under another one.
export const accountSessionsOptions = (accountId: string) =>
  queryOptions({
    queryKey: ["account-sessions", accountId],
    queryFn: ({ signal }) =>
      result(client.GET("/api/v1/auth/sessions", { signal })),
  });
