import { useQuery } from "@tanstack/react-query";
import { client, result, type Schema } from "../api/client";

export type PostgresRelease = Schema["PostgresRelease"];

// The server owns the PostgreSQL pins. A new database takes its major's current release, and
// a database on a retired release keeps it until its owner stages the in-place update.
export function usePostgresReleases(workspaceId: string) {
  return useQuery({
    queryKey: ["postgres-releases", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/postgres-releases", {
          params: { path: { workspaceId } },
        }),
      ),
    staleTime: Number.POSITIVE_INFINITY,
    // The list rarely fails, but without it no database can be added; try again before saying so.
    retry: 2,
  });
}

export function currentReleases(releases: PostgresRelease[]) {
  return releases
    .filter((release) => release.current)
    .toSorted((a, b) => b.major - a.major);
}

// The release a database should move to, when it runs a retired one.
export function availableUpdate(
  releases: PostgresRelease[],
  image: string | undefined,
) {
  const running = releases.find((release) => release.image === image);
  if (!running || running.current) return undefined;
  return releases.find(
    (release) => release.current && release.major === running.major,
  );
}
