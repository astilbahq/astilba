import { useQuery } from "@tanstack/react-query";
import { client, result } from "../api/client";
import { Button } from "../components/button";
import * as s from "../styles";
import { ErrorNotice } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;

// Recent applied releases, each application's rollback target and what rolling it back would
// change, including the secrets it would return to an earlier version. Creating the draft is the
// caller's, which loads it into the editor.
export function ReleaseHistory({
  path,
  revisionId,
  write,
  disabled,
  onRollback,
}: {
  path: EnvironmentPath;
  revisionId: string;
  write: boolean;
  disabled: boolean;
  onRollback: (planId: string, applicationId: string) => void;
}) {
  const releases = useQuery({
    queryKey: ["releases", path, revisionId],
    queryFn: () => result(client.GET(`${root}/releases`, { params: { path } })),
    refetchInterval: 10000,
  });
  return (
    <section className={s.panel} aria-label="Release history">
      <h2>Release history</h2>
      <p className={s.muted}>
        Rollback creates a new draft for review. Preparing its deployment plan
        checks the retained image. Release commands are not copied, and database
        data is never rolled back.
      </p>
      <ErrorNotice error={releases.error} />
      {releases.isPending && <p role="status">Loading release history…</p>}
      {releases.data?.length === 0 && (
        <p>Successfully applied deployments will appear here.</p>
      )}
      {releases.data?.map((entry) => (
        <details key={entry.planId}>
          <summary>
            Revision {entry.revisionId.slice(0, 8)} ·{" "}
            {new Date(entry.createdAt).toLocaleString()}
          </summary>
          <ul>
            {entry.applications.map((app) => (
              <li key={app.id}>
                <strong>{app.name}</strong>
                <code className={s.code}>{app.image}</code>
                <p>{app.note}</p>
                {app.changes.length > 0 && (
                  <p>Changed settings: {app.changes.join(", ")}.</p>
                )}
                {app.olderSecretVersions.length > 0 && (
                  <div>
                    <p>
                      Rolling back returns these secrets to an earlier version.
                      Files you submit and public build arguments are checked
                      against every version the draft uses.
                    </p>
                    <ul>
                      {app.olderSecretVersions.map((secret) => (
                        <li key={secret.versionId}>
                          <bdi>{secret.name}</bdi>{" "}
                          <code className={s.code}>{secret.versionId}</code>
                          {secret.retired &&
                            " (retired: its value is erased, so replace it before planning)"}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
                {write && (
                  <Button
                    type="button"
                    disabled={!app.eligible || disabled}
                    onClick={() => onRollback(entry.planId, app.id)}
                  >
                    Create rollback draft for {app.name}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </details>
      ))}
    </section>
  );
}
