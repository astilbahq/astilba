import { queryOptions } from "@tanstack/react-query";
import { client, result } from "./client";

export const backupDestinationsPath =
  "/api/v1/workspaces/{workspaceId}/backup-destinations" as const;
export const backupPoliciesPath =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/backup-policies" as const;
export const backupPolicyPath = `${backupPoliciesPath}/{policyId}` as const;
// The API's "none" for a policy's destination or a run's target.
export const nilID = "00000000-0000-0000-0000-000000000000";

export const backupDestinationsOptions = (workspaceId: string) =>
  queryOptions({
    queryKey: ["backup-destinations", workspaceId],
    queryFn: () =>
      result(
        client.GET(backupDestinationsPath, {
          params: { path: { workspaceId } },
        }),
      ),
  });

// AWS admin credentials management mints a managed destination's per-server keys with.
export const backupStorageAccountsPath =
  "/api/v1/workspaces/{workspaceId}/backup-storage-accounts" as const;
export const backupStorageAccountPath =
  `${backupStorageAccountsPath}/{accountId}` as const;
export const backupDestinationPath =
  `${backupDestinationsPath}/{destinationId}` as const;

export const backupStorageAccountsOptions = (workspaceId: string) =>
  queryOptions({
    queryKey: ["backup-storage-accounts", workspaceId],
    queryFn: () =>
      result(
        client.GET(backupStorageAccountsPath, {
          params: { path: { workspaceId } },
        }),
      ),
  });
