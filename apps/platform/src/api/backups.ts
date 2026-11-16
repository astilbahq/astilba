export const backupDestinationsPath =
  "/api/v1/workspaces/{workspaceId}/backup-destinations" as const;
export const backupPoliciesPath =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/backup-policies" as const;
export const backupPolicyPath = `${backupPoliciesPath}/{policyId}` as const;
// The API's "none" for a policy's destination or a run's target.
export const nilID = "00000000-0000-0000-0000-000000000000";
