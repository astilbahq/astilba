import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { client, result, type Schema } from "../api/client";
import * as s from "../styles";
import { ErrorNotice, Field, Form, text, useAction } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;
export function Deployments({
  path,
  revision,
  dirty,
  write,
  owner,
  removeVolume,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  dirty: boolean;
  write: boolean;
  owner: boolean;
  removeVolume: (id: string) => void;
}) {
  const [serverId, setServerId] = useState("");
  const [planId, setPlanId] = useState("");
  const [key, setKey] = useState("");
  const [message, setMessage] = useState("");
  const [bundle, setBundle] = useState<Schema["ConfigurationExport"]>();
  const { workspaceId, projectId, environmentId } = path;
  const action = useAction();
  const queries = useQueryClient();
  const servers = useQuery({
    queryKey: ["servers", path.workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/servers", {
          params: { path: { workspaceId: path.workspaceId } },
        }),
      ),
    refetchInterval: 15000,
  });
  const plan = useQuery({
    queryKey: ["visual-plan", path, planId],
    enabled: !!planId,
    queryFn: () =>
      result(
        client.GET(`${root}/deployment-plans/{planId}`, {
          params: { path: { ...path, planId } },
        }),
      ),
    refetchInterval: (query) =>
      query.state.data?.plan ||
      ["failed", "cancelled"].includes(query.state.data?.state ?? "")
        ? false
        : 2000,
  });
  const runtime = useQuery({
    queryKey: ["runtime", path],
    queryFn: () => result(client.GET(`${root}/runtime`, { params: { path } })),
    refetchInterval: 5000,
  });
  const volumes = useQuery({
    queryKey: ["volumes", path],
    queryFn: () => result(client.GET(`${root}/volumes`, { params: { path } })),
    refetchInterval: 5000,
  });
  const operations = useQuery({
    queryKey: ["operations", path.workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/operations", {
          params: { path: { workspaceId: path.workspaceId } },
        }),
      ),
    refetchInterval: 3000,
  });
  useEffect(() => {
    const events = new EventSource(
      `/api/v1/workspaces/${workspaceId}/projects/${projectId}/environments/${environmentId}/events`,
    );
    events.addEventListener("changed", () => {
      void queries.invalidateQueries({
        queryKey: ["runtime", { workspaceId, projectId, environmentId }],
      });
      void queries.invalidateQueries({
        queryKey: ["operations", workspaceId],
      });
    });
    return () => events.close();
  }, [workspaceId, projectId, environmentId, queries]);
  const ready = plan.data?.plan;
  const stale = !!ready && ready.revisionId !== revision.id;
  const download = (name: string, data: string) => {
    const url = URL.createObjectURL(
      new Blob([data], { type: "text/plain;charset=utf-8" }),
    );
    const link = document.createElement("a");
    link.href = url;
    link.download = name;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  return (
    <section className={s.stack} aria-label="Deployment review and runtime">
      <section className={s.panel}>
        <h2>Review and deploy</h2>
        <p className={s.muted}>
          A plan freezes the saved revision, image digests, placement and secret
          versions. Applying it is a separate action.
        </p>
        <ErrorNotice error={action.error ?? servers.error ?? plan.error} />
        {message && <p role="status">{message}</p>}
        {write && (
          <Form
            action={action}
            label="Prepare deployment plan"
            onSubmit={(d) =>
              action.run(async () => {
                const next = await result(
                  client.POST(`${root}/deployment-plans`, {
                    params: { path },
                    body: {
                      baseRevisionId: revision.id,
                      serverId,
                      allowHostChanges: d.has("host"),
                      acceptDrift: d.has("drift"),
                    },
                  }),
                );
                setPlanId(next.id);
                setKey(crypto.randomUUID());
                setMessage(
                  "Resolving image identities and inspecting current state. No deployment has started.",
                );
              })
            }
          >
            <label>
              Deployment server
              <select
                value={serverId}
                onChange={(e) => setServerId(e.target.value)}
                required
              >
                <option value="">Choose an enrolled server</option>
                {servers.data
                  ?.filter((v) => v.role !== "build" && v.status !== "revoked")
                  .map((v) => (
                    <option
                      key={v.id}
                      value={v.id}
                      disabled={
                        !v.capabilities?.visual ||
                        (!!revision.specification.compose &&
                          !v.capabilities?.compose)
                      }
                    >
                      {v.name}
                      {v.capabilities?.visual &&
                      (!revision.specification.compose ||
                        v.capabilities?.compose)
                        ? ""
                        : " — supported Compose required"}
                    </option>
                  ))}
              </select>
            </label>
            {owner && (
              <label className={s.row}>
                <input type="checkbox" name="host" />
                Approve the host permissions listed in the plan, including
                proxy, ports, certificates and elevated container privileges
              </label>
            )}
            <label className={s.row}>
              <input type="checkbox" name="drift" />
              Reconcile reviewed manual changes to Astilba-managed resources
            </label>
            <button
              type="submit"
              disabled={dirty || action.isPending || !serverId}
            >
              Prepare plan
            </button>
            {dirty && (
              <small>
                Save your staged changes before preparing or applying a plan.
              </small>
            )}
          </Form>
        )}
        {plan.data && !ready && (
          <p role="status">
            Plan: {plan.data.state}. View the resolution operation below for
            details.
          </p>
        )}
        {ready && (
          <section className={s.stack} aria-label="Reviewed deployment plan">
            <h3>Plan ready</h3>
            <p>
              Server:{" "}
              {servers.data?.find((server) => server.id === ready.serverId)
                ?.name ?? ready.serverId}
            </p>
            <ul>
              {ready.effects.map((effect) => (
                <li key={effect}>{effect}</li>
              ))}
            </ul>
            <p>
              Expires {new Date(ready.expiresAt).toLocaleTimeString()} ·
              revision {ready.revisionId.slice(0, 8)}
            </p>
            <ul>
              {ready.specification.applications.map((app) => (
                <li key={app.id}>
                  <strong>{app.name}</strong>
                  <code className={s.code}> {ready.images[app.id]}</code>
                </li>
              ))}
            </ul>
            <p>
              {ready.specification.bindings?.length ?? 0} database bindings.
              Secret values are withheld; the plan uses immutable version
              references.
            </p>
            {stale && (
              <p role="alert">
                The saved revision changed. Prepare a new plan.
              </p>
            )}
            {write && (
              <button
                type="button"
                className={s.primary}
                disabled={
                  dirty ||
                  stale ||
                  action.isPending ||
                  Date.now() > Date.parse(ready.expiresAt)
                }
                onClick={() =>
                  action.run(async () => {
                    const op = await result(
                      client.POST(`${root}/deployment-plans/{planId}/apply`, {
                        params: { path: { ...path, planId } },
                        body: { idempotencyKey: key },
                      }),
                    );
                    setMessage(
                      `Deployment ${op.state}. It continues if this page closes.`,
                    );
                    await queries.invalidateQueries({
                      queryKey: ["operations", path.workspaceId],
                    });
                    await queries.invalidateQueries({
                      queryKey: [
                        "environments",
                        path.workspaceId,
                        path.projectId,
                      ],
                    });
                  })
                }
              >
                Apply reviewed plan
              </button>
            )}
          </section>
        )}
      </section>
      <section className={s.panel}>
        <div className={s.spread}>
          <h2>Runtime observations</h2>
          {write && (
            <button
              type="button"
              disabled={action.isPending}
              onClick={() =>
                action.run(async () => {
                  await result(
                    client.POST(`${root}/runtime/refresh`, {
                      params: { path },
                    }),
                  );
                  setMessage(
                    "Runtime inspection queued; resources will not be changed.",
                  );
                })
              }
            >
              Inspect host now
            </button>
          )}
        </div>
        <ErrorNotice error={runtime.error} />
        <p className={s.muted}>
          {runtime.data && Date.parse(runtime.data.observedAt) > 0
            ? `Observed ${new Date(runtime.data.observedAt).toLocaleString()}`
            : "No host observations yet."}{" "}
          Opening this page does not reconcile Docker changes.
        </p>
        {runtime.data?.infrastructureDrift && (
          <p role="alert">
            The ingress configuration has changed outside Astilba.
          </p>
        )}
        {!!runtime.data?.omittedResources && (
          <p role="status">
            {runtime.data.omittedResources} additional retained services are
            included in drift checks. Review a reconciliation plan to remove
            retired services.
          </p>
        )}
        <ul className={s.list}>
          {runtime.data?.resources.map((resource) => (
            <li key={resource.id}>
              <strong>
                {revision.specification.applications.find(
                  (a) => a.id === resource.id,
                )?.name ?? "Retired service"}
              </strong>{" "}
              · {resource.state}
              {resource.drift && " · Drift detected"}
              <p className={s.code}>{resource.image}</p>
              {resource.memoryBytes > 0 && (
                <small>
                  {(resource.memoryBytes / 1048576).toFixed(1)} MiB in use
                </small>
              )}
              {resource.note && <p>{resource.note}</p>}
              {resource.logs && (
                <details>
                  <summary>Recent logs</summary>
                  <pre className={s.log}>{resource.logs}</pre>
                </details>
              )}
            </li>
          ))}
        </ul>
      </section>
      <section className={s.panel}>
        <h2>Operations</h2>
        <ErrorNotice error={operations.error} />
        <ol className={s.list}>
          {operations.data
            ?.filter((o) => o.environmentId === path.environmentId)
            .slice(0, 10)
            .map((op) => (
              <li key={op.id}>
                <div className={s.spread}>
                  <strong>
                    {op.kind} · {op.state}
                  </strong>
                  <time dateTime={op.createdAt}>
                    {new Date(op.createdAt).toLocaleTimeString()}
                  </time>
                </div>
                <p>{op.note}</p>
                <ol>
                  {op.steps.map((step) => (
                    <li key={step.name}>
                      {step.name}: {step.state}
                    </li>
                  ))}
                </ol>
                {write &&
                  ["pending", "dispatched", "running"].includes(op.state) && (
                    <button
                      type="button"
                      onClick={() =>
                        action.run(async () => {
                          await result(
                            client.POST(
                              "/api/v1/workspaces/{workspaceId}/operations/{operationId}/cancel",
                              {
                                params: {
                                  path: {
                                    workspaceId: path.workspaceId,
                                    operationId: op.id,
                                  },
                                },
                              },
                            ),
                          );
                          setMessage(
                            "Cancellation requested at the next safe checkpoint; existing data is retained.",
                          );
                        })
                      }
                    >
                      Request cancellation
                    </button>
                  )}
              </li>
            ))}
        </ol>
      </section>
      <details className={s.panel}>
        <summary>Clone, export and data volumes</summary>
        <p>
          Clones receive new service identities and empty storage. Export
          includes configuration and secret placeholders; restore data
          separately.
        </p>
        {write && (
          <Form
            action={action}
            label="Clone environment"
            onSubmit={(d, form) =>
              action.run(async () => {
                const clone = await result(
                  client.POST(`${root}/clone`, {
                    params: { path },
                    body: { name: text(d, "name"), copySecrets: d.has("copy") },
                  }),
                );
                setMessage(
                  `Created ${clone.name} with independent storage and bindings.`,
                );
                form.reset();
                await queries.invalidateQueries({
                  queryKey: ["environments", path.workspaceId, path.projectId],
                });
              })
            }
          >
            <Field name="name" label="Clone name" />
            <label className={s.row}>
              <input type="checkbox" name="copy" />
              Copy secret values into independent secrets (required for Compose
              sources with secret references; requires recent authentication)
            </label>
            {revision.specification.compose &&
              !!revision.specification.routes?.length && (
                <p>
                  Source-defined routes must be changed explicitly. Create a
                  Compose environment and copy the bundle with new routes.
                </p>
              )}
            <button
              type="submit"
              disabled={
                !!revision.specification.compose &&
                !!revision.specification.routes?.length
              }
            >
              Clone saved configuration
            </button>
          </Form>
        )}
        <button
          type="button"
          disabled={action.isPending}
          onClick={() =>
            action.run(async () =>
              setBundle(
                await result(
                  client.GET(`${root}/export`, { params: { path } }),
                ),
              ),
            )
          }
        >
          Prepare export of last applied configuration
        </button>
        {bundle && (
          <>
            <pre className={s.log}>{bundle.readme}</pre>
            <div className={s.row}>
              <a
                href={`/api/v1/workspaces/${workspaceId}/projects/${projectId}/environments/${environmentId}/export/archive`}
                download
              >
                Download complete bundle
              </a>
              <button
                type="button"
                onClick={() => download("compose.json", bundle.compose)}
              >
                Download Compose
              </button>
              <button
                type="button"
                onClick={() => download("routes.yaml", bundle.routes)}
              >
                Download routes
              </button>
              <button
                type="button"
                onClick={() => download("README.txt", bundle.readme)}
              >
                Download instructions
              </button>
            </div>
          </>
        )}
        <ul className={s.list}>
          {volumes.data?.map((volume) => {
            const used = volume.inUse;
            return (
              <li key={volume.id}>
                {volume.name} ·{" "}
                {volume.deleted ? "Deleted" : used ? "Attached" : "Retained"}
                {owner && (
                  <button
                    type="button"
                    disabled={
                      used || volume.deleted || dirty || action.isPending
                    }
                    onClick={() => {
                      if (
                        window.confirm(
                          `Permanently delete all data in ${volume.name}? This cannot be undone.`,
                        )
                      )
                        action.run(async () => {
                          await result(
                            client.DELETE(`${root}/volumes/{volumeId}`, {
                              params: {
                                path: { ...path, volumeId: volume.id },
                              },
                            }),
                          );
                          removeVolume(volume.id);
                          setMessage(
                            revision.specification.compose
                              ? "Data-volume deletion queued. Check its result, then remove the unused volume declaration from your source files before deploying again."
                              : "Data-volume deletion queued and removal staged. Check the operation result, then save the configuration revision.",
                          );
                        });
                    }}
                  >
                    Delete data volume permanently
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      </details>
    </section>
  );
}
