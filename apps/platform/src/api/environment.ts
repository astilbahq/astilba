import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { client, result, type Schema } from "./client";

export type EnvironmentPath = {
  workspaceId: string;
  projectId: string;
  environmentId: string;
};
const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}";

export const draftOptions = (path: EnvironmentPath) =>
  queryOptions({
    queryKey: ["draft", path],
    queryFn: ({ signal }) =>
      result(client.GET(`${root}/draft`, { params: { path }, signal })),
  });
export const runtimeOptions = (path: EnvironmentPath) =>
  queryOptions({
    queryKey: ["runtime", path],
    queryFn: ({ signal }) =>
      result(client.GET(`${root}/runtime`, { params: { path }, signal })),
    refetchInterval: 5000,
  });

// Only current-draft reads and successful writes belong here, never history.
export async function publishDraft(
  queries: QueryClient,
  path: EnvironmentPath,
  revision: Schema["Revision"],
) {
  const { queryKey } = draftOptions(path);
  // Await settlement too: an already-fulfilled read can still be committing.
  await queries.cancelQueries({ queryKey, exact: true });
  queries.setQueryData(queryKey, revision);
}
