import { queryOptions } from "@tanstack/react-query";
import { client, result } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";

export const policyPath =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/troubleshooting";

export const policyOptions = (path: EnvironmentPath) =>
  queryOptions({
    queryKey: ["troubleshooting-policy", path],
    queryFn: ({ signal }) =>
      result(client.GET(policyPath, { params: { path }, signal })),
  });

// Closing sessions and changing the setting affect both lists below.
export const sessionsKey = (workspaceId: string) => [
  "troubleshooting-sessions",
  workspaceId,
];

export const openSessionsOptions = (path: EnvironmentPath) =>
  queryOptions({
    queryKey: [...sessionsKey(path.workspaceId), "open", path.environmentId],
    queryFn: ({ signal }) =>
      result(
        client.GET(`${policyPath}/sessions`, { params: { path }, signal }),
      ),
    // Fetching a list that never loaded clears its error, so polling would make the
    // failure flicker and announce again. A person retries that failure instead.
    refetchInterval: ({ state }) =>
      state.status === "error" && state.data === undefined ? false : 5000,
  });

export const historyOptions = (workspaceId: string) =>
  queryOptions({
    queryKey: [...sessionsKey(workspaceId), "history"],
    queryFn: ({ signal }) =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/troubleshooting-sessions",
          {
            params: { path: { workspaceId } },
            signal,
          },
        ),
      ),
  });
