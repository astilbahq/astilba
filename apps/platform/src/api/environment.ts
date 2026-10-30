import {
  queryOptions,
  type QueryClient,
  type QueryState,
} from "@tanstack/react-query";
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

export const layoutOptions = (path: EnvironmentPath) =>
  queryOptions({
    queryKey: ["layout", path],
    queryFn: ({ signal }) =>
      result(client.GET(`${root}/layout`, { params: { path }, signal })),
  });

export async function readCurrentDraft(
  queries: QueryClient,
  path: EnvironmentPath,
) {
  await queries.cancelQueries({
    queryKey: draftOptions(path).queryKey,
    exact: true,
  });
  return queries.fetchQuery({ ...draftOptions(path), staleTime: 0 });
}

export async function publishLayout(
  queries: QueryClient,
  path: EnvironmentPath,
  layout: Schema["Layout"],
  previous: QueryState<Schema["Layout"]> | undefined,
) {
  const { queryKey } = layoutOptions(path);
  await queries.cancelQueries({ queryKey, exact: true });
  // Equal-valued reads can share data, but still produce a new query state.
  if (queries.getQueryState(queryKey) === previous) {
    queries.setQueryData(queryKey, layout);
  } else {
    await queries.invalidateQueries({
      queryKey,
      exact: true,
      refetchType: "all",
    });
  }
}

// Only current-draft reads and successful writes belong here, never history.
export async function publishDraft(
  queries: QueryClient,
  path: EnvironmentPath,
  revision: Schema["Revision"],
  baseRevisionId: string,
) {
  const { queryKey } = draftOptions(path);
  // Await settlement too: an already-fulfilled read can still be committing.
  await queries.cancelQueries({ queryKey, exact: true });
  const current = queries.getQueryData(queryKey);
  // A newer visit or write may have completed while this response was in flight.
  if (current && current.id !== baseRevisionId && current.id !== revision.id) {
    await queries.invalidateQueries({
      queryKey,
      exact: true,
      refetchType: "all",
    });
    return;
  }
  queries.setQueryData(queryKey, revision);
}
