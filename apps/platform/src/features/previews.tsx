import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, client, result, type Schema } from "../api/client";
import { previewsOptions } from "../api/environment";
import { Button } from "../components/button";
import { Link } from "../components/link";
import * as s from "../styles";
import { ErrorNotice, useAction } from "../ui";
import { PreviewPolicyEditor } from "./preview-policy-editor";
import * as previewStyles from "./previews.styles";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;
// `owner` manages the policy and each preview; `cleanup` only removes previews. A retired template
// gives its owners cleanup alone, which its retirement may have left blocked.
export function Previews({
  path,
  revision,
  dirty,
  owner,
  cleanup,
  retired,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  dirty: boolean;
  owner: boolean;
  cleanup: boolean;
  /** The template is retiring or retired: its policy is off and it creates no new previews. */
  retired: boolean;
}) {
  const query = useQueryClient();
  const action = useAction();
  const policy = useQuery({
    queryKey: ["preview-policy", path],
    queryFn: async () => {
      try {
        return await result(
          client.GET(`${root}/preview-policy`, { params: { path } }),
        );
      } catch (error) {
        if (error instanceof ApiError && error.detail.code === "not_found")
          return null;
        throw error;
      }
    },
  });
  const previews = useQuery(previewsOptions(path));
  const refresh = async () => {
    await query.invalidateQueries({ queryKey: previewsOptions(path).queryKey });
    await query.invalidateQueries({ queryKey: ["preview-policy", path] });
    await query.invalidateQueries({
      queryKey: ["environments", path.workspaceId, path.projectId],
    });
  };
  const run = (
    p: Schema["PullRequestPreview"],
    kind: "approve" | "cleanup" | "refresh",
  ) =>
    action.run(async () => {
      await result(
        client.POST(`${root}/previews/{previewId}/actions`, {
          params: { path: { ...path, previewId: p.id } },
          body: {
            action: kind,
            head: p.head,
            headRepository: p.headRepository,
          },
        }),
      );
      await refresh();
    });
  const active =
    previews.data?.some((p) => !!p.environmentId && p.state !== "deleted") ??
    false;
  return (
    <section className={s.panel} aria-label="Pull-request previews">
      <h2>Pull-request previews</h2>
      <p>
        Previews use this template’s saved configuration and preview-only
        secrets. Every preview gets separate services, storage and a new
        database password.{" "}
        {retired
          ? "This template is retired, so it creates no new previews; remove the ones that remain."
          : "Template edits take effect only after an owner saves a new policy."}
      </p>
      <ErrorNotice error={policy.error || previews.error || action.error} />
      {action.isPending && <p role="status">Updating preview…</p>}
      {policy.data?.note && <p className={s.notice}>{policy.data.note}</p>}
      {policy.isPending ? (
        <p role="status">Loading preview policy…</p>
      ) : policy.data ? (
        <p className={s.notice}>
          {policy.data.enabled
            ? "Automatic previews enabled"
            : "Automatic previews paused"}
          .{" "}
          {policy.data.configuration.closeGraceHours === null
            ? "Closed previews are retained until owner cleanup."
            : `Closed previews and their runtime data are removed after ${policy.data.configuration.closeGraceHours} hours.`}
        </p>
      ) : (
        <p>No preview policy configured.</p>
      )}
      {owner && !policy.isPending && !policy.error && (
        <>
          {policy.data?.enabled && (
            <Button
              type="button"
              disabled={action.isPending}
              onClick={() =>
                action.run(async () => {
                  const saved = policy.data;
                  if (!saved) return;
                  await result(
                    client.PUT(`${root}/preview-policy`, {
                      params: { path },
                      body: {
                        enabled: false,
                        generation: saved.generation,
                        configuration: saved.configuration,
                      },
                    }),
                  );
                  await refresh();
                })
              }
            >
              Pause automatic previews
            </Button>
          )}
          {policy.data && !policy.data.enabled && active && (
            <Button
              type="button"
              disabled={action.isPending}
              onClick={() =>
                action.run(async () => {
                  const saved = policy.data;
                  if (!saved) return;
                  await result(
                    client.PUT(`${root}/preview-policy`, {
                      params: { path },
                      body: {
                        enabled: true,
                        generation: saved.generation,
                        configuration: saved.configuration,
                      },
                    }),
                  );
                  await refresh();
                })
              }
            >
              Resume saved preview policy
            </Button>
          )}
          {active && (
            <p>
              Clean up active previews before changing the policy. Pausing stops
              new builds; previews already running retain their data.
            </p>
          )}
          <PreviewPolicyEditor
            key={policy.data?.generation ?? 0}
            path={path}
            revision={revision}
            policy={policy.data ?? undefined}
            disabled={active || dirty}
            refresh={refresh}
          />
        </>
      )}
      {previews.isPending ? (
        <p role="status">Loading previews…</p>
      ) : previews.data?.length ? (
        <ul className={s.list}>
          {previews.data.map((p) => (
            <li key={p.id} className={previewStyles.preview}>
              <div className={s.spread}>
                <h3>Request #{p.number}</h3>
                <span>{p.state.replaceAll("_", " ")}</span>
              </div>
              <p>
                Commit{" "}
                <code className={previewStyles.commit}>
                  {p.head || "Awaiting provider verification"}
                </code>
                {p.fork
                  ? ` · Fork · Source repository ID ${p.headRepository}`
                  : ""}
              </p>
              {p.note && <p>{p.note}</p>}
              {p.cleanupAt && (
                <p>
                  Cleanup scheduled for {new Date(p.cleanupAt).toLocaleString()}
                  . This removes this preview’s runtime data.
                </p>
              )}
              <div className={s.row}>
                {p.environmentId && (
                  <Link
                    to="/"
                    search={{
                      workspace: path.workspaceId,
                      project: path.projectId,
                      environment: p.environmentId,
                    }}
                  >
                    Open preview environment
                  </Link>
                )}
                {p.operationId && (
                  <Link to="/servers" search={{ workspace: path.workspaceId }}>
                    Inspect server operations
                  </Link>
                )}
              </div>
              {(owner || cleanup) && (
                <div className={s.row}>
                  {owner && p.state === "approval_required" && (
                    <Button
                      type="button"
                      disabled={action.isPending}
                      onClick={() => {
                        if (
                          confirm(
                            `Approve fork commit ${p.head} from source repository ID ${p.headRepository}? Its code will run on the preview host and receive this template’s preview secrets.`,
                          )
                        )
                          run(p, "approve");
                      }}
                    >
                      Approve this fork commit
                    </Button>
                  )}
                  {owner && (
                    <Button
                      type="button"
                      disabled={action.isPending}
                      onClick={() => run(p, "refresh")}
                    >
                      {p.state === "deleted"
                        ? "Recreate preview"
                        : "Refresh preview"}
                    </Button>
                  )}
                  {cleanup &&
                    p.state !== "deleted" &&
                    p.state !== "deleting" && (
                      <Button
                        type="button"
                        variant="danger"
                        disabled={action.isPending}
                        onClick={() => {
                          if (
                            confirm(
                              `Remove request #${p.number}’s preview now, including its owned volumes and database data? This cannot be undone.`,
                            )
                          )
                            run(p, "cleanup");
                        }}
                      >
                        Remove preview and data
                      </Button>
                    )}
                </div>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p>
          {retired
            ? "No previews remain."
            : "No previews yet. Astilba discovers open requests and listens for signed provider events. Fork commits wait for owner approval."}
        </p>
      )}
    </section>
  );
}
