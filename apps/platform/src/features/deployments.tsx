import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { client, result, type Schema } from "../api/client";
import { runtimeOptions } from "../api/environment";
import { publishDraft } from "../api/environment";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Anchor } from "../components/link";
import { Select } from "../components/select";
import { Textarea } from "../components/textarea";
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
  loadRevision,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  dirty: boolean;
  write: boolean;
  owner: boolean;
  removeVolume: (id: string) => void;
  loadRevision: (revision: Schema["Revision"]) => void;
}) {
  const [requireRelease, setRequireRelease] = useState(false);
  const [releaseCommand, setReleaseCommand] = useState("");
  const [serverId, setServerId] = useState("");
  const [planId, setPlanId] = useState("");
  const [key, setKey] = useState("");
  const [message, setMessage] = useState("");
  const [bundle, setBundle] = useState<Schema["ConfigurationExport"]>();
  const [logHours, setLogHours] = useState<1 | 6 | 24>(1);
  const logExport = useAction();
  const [logNotice, setLogNotice] = useState("");
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
  const runtime = useQuery(runtimeOptions(path));
  const releases = useQuery({
    queryKey: ["releases", path, revision.id],
    queryFn: () => result(client.GET(`${root}/releases`, { params: { path } })),
    refetchInterval: 10000,
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
  const download = (
    name: string,
    data: string,
    type = "text/plain;charset=utf-8",
  ) => {
    const url = URL.createObjectURL(new Blob([data], { type }));
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
                      ...(requireRelease
                        ? {
                            releaseOptions: {
                              command: text(d, "releaseCommand").trim()
                                ? text(d, "releaseCommand").split("\n")
                                : [],
                              commandTimeoutSeconds: Number(
                                text(d, "releaseTimeout"),
                              ),
                              drainSeconds: Number(text(d, "releaseDrain")),
                            },
                          }
                        : {}),
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
            <Label>
              Deployment server
              <Select
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
              </Select>
            </Label>
            {owner && (
              <Label className={s.row}>
                <Checkbox name="host" />
                Approve the host permissions listed in the plan, including
                proxy, ports, certificates and elevated container privileges
              </Label>
            )}
            <Label className={s.row}>
              <Checkbox name="drift" />
              Reconcile reviewed manual changes to Astilba-managed resources
            </Label>
            <details>
              <summary>Protected release options</summary>
              <p>
                Eligible changes use overlapping instances automatically on
                supported agents. Require that strategy to set a command or
                drain budget; incompatible changes will be rejected.
              </p>
              <Label className={s.row}>
                <Checkbox
                  checked={requireRelease}
                  onChange={(e) => setRequireRelease(e.target.checked)}
                />
                Require a protected stateless HTTP release
              </Label>
              {requireRelease && (
                <>
                  <Label>
                    Release command (one argument per line)
                    <Textarea
                      name="releaseCommand"
                      rows={4}
                      value={releaseCommand}
                      onChange={(event) =>
                        setReleaseCommand(event.target.value)
                      }
                      disabled={action.isPending}
                      aria-describedby="release-command-help"
                    />
                  </Label>
                  <p id="release-command-help" className={s.muted}>
                    Optional. Runs in the target image before readiness. A shell
                    runs only if supplied as an explicit executable. Database
                    writes and migrations cannot be undone by image rollback.
                  </p>
                  <Label>
                    Command timeout (seconds)
                    <Input
                      name="releaseTimeout"
                      type="number"
                      min={1}
                      max={300}
                      defaultValue={60}
                      required
                    />
                  </Label>
                  <Label>
                    Connection drain (seconds)
                    <Input
                      name="releaseDrain"
                      type="number"
                      min={1}
                      max={120}
                      defaultValue={10}
                      required
                    />
                  </Label>
                </>
              )}
            </details>
            <Button
              type="submit"
              disabled={dirty || action.isPending || !serverId}
            >
              Prepare plan
            </Button>
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
              <strong>
                {ready.distributed
                  ? "Coordinated cross-host release"
                  : ready.release
                    ? "Protected stateless HTTP release"
                    : "Standard deployment"}
              </strong>
            </p>
            {ready.release && (
              <p>
                Readiness must pass before traffic moves. Drain:{" "}
                {ready.release.options.drainSeconds} seconds. Release command:{" "}
                {ready.release.options.command.length
                  ? ready.release.options.command.join(" · ")
                  : "none"}
                .
              </p>
            )}
            <p>
              Server:{" "}
              {servers.data?.find((server) => server.id === ready.serverId)
                ?.name ?? ready.serverId}
            </p>
            <ul>
              {ready.specification.applications.map((app) => (
                <li key={app.id}>
                  {app.name}: {app.replicas ?? 1}{" "}
                  {(app.replicas ?? 1) === 1 ? "instance" : "instances"}
                </li>
              ))}
            </ul>
            {ready.distributed && (
              <section className={s.stack} aria-label="Placement changes">
                <h4>Reviewed placements</h4>
                {ready.distributed.units.map((unit) => (
                  <div key={unit.plan.unitId}>
                    <strong>
                      {servers.data?.find(
                        (server) => server.id === unit.plan.serverId,
                      )?.name ?? unit.plan.serverId}
                      {unit.plan.local?.primary
                        ? " · ingress and persistent services"
                        : ""}
                    </strong>
                    <ul>
                      {ready.specification.applications.map((app) => (
                        <li key={app.id}>
                          {app.name}:{" "}
                          {unit.plan.local?.priorInstances[app.id]?.length ?? 0}{" "}
                          → {unit.plan.local?.instances[app.id]?.length ?? 0}{" "}
                          instances
                          {unit.plan.images[app.id] && (
                            <code className={s.code}>
                              {" "}
                              · {unit.plan.images[app.id]}
                            </code>
                          )}
                        </li>
                      ))}
                    </ul>
                    <p>
                      New pool: {unit.cpuMillis} millicores · {unit.memoryMiB}{" "}
                      MiB. Serving pools remain reserved until publication is
                      confirmed.
                    </p>
                  </div>
                ))}
              </section>
            )}
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
              {!ready.distributed &&
                ready.specification.applications.map((app) => (
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
            <details>
              <summary>Effective configuration and secret references</summary>
              <pre className={`${s.code} ${s.wrappedCode}`}>
                {JSON.stringify(ready.specification, null, 2)}
              </pre>
            </details>
            {stale && (
              <p role="alert">
                The saved revision changed. Prepare a new plan.
              </p>
            )}
            {write && (
              <Button
                type="button"
                variant="primary"
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
              </Button>
            )}
          </section>
        )}
      </section>
      <section className={s.panel} aria-label="Release history">
        <h2>Release history</h2>
        <p className={s.muted}>
          Rollback creates a new draft for review. Preparing its deployment plan
          checks the retained image. Release commands are not copied, and
          database data is never rolled back.
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
                  {write && (
                    <Button
                      type="button"
                      disabled={!app.eligible || dirty || action.isPending}
                      onClick={() =>
                        action.run(async () => {
                          const next = await result(
                            client.POST(`${root}/releases/{planId}/rollback`, {
                              params: {
                                path: { ...path, planId: entry.planId },
                              },
                              body: {
                                baseRevisionId: revision.id,
                                applicationId: app.id,
                              },
                            }),
                          );
                          await publishDraft(queries, path, next, revision.id);
                          loadRevision(next);
                          setRequireRelease(true);
                          setReleaseCommand("");
                          setPlanId("");
                          setMessage(
                            "Rollback draft created. Review its effective configuration and prepare a new deployment plan; nothing has been deployed.",
                          );
                          await queries.invalidateQueries({
                            queryKey: ["revisions", path],
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
                      Create rollback draft for {app.name}
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          </details>
        ))}
      </section>
      <section className={s.panel}>
        <div className={s.spread}>
          <h2>Runtime observations</h2>
          {write && (
            <Button
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
                    "Runtime inspection queued for the active deployment hosts; resources will not be changed.",
                  );
                })
              }
            >
              Inspect runtime
            </Button>
          )}
        </div>
        <ErrorNotice error={runtime.error} />
        <p className={s.muted}>
          {runtime.data && Date.parse(runtime.data.observedAt) > 0
            ? `Observed ${new Date(runtime.data.observedAt).toLocaleString()}`
            : "No host observations yet."}{" "}
          Opening this page does not reconcile Docker changes.
        </p>
        {!!runtime.data?.hosts?.length && (
          <ul className={s.list} aria-label="Runtime snapshots by server">
            {runtime.data.hosts.map((host) => (
              <li key={host.unitId}>
                <strong>
                  {servers.data?.find((server) => server.id === host.serverId)
                    ?.name ?? host.serverId}
                </strong>{" "}
                · {host.state} · observed{" "}
                {new Date(host.observedAt).toLocaleString()}
              </li>
            ))}
          </ul>
        )}
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
              {!!resource.instances?.length && (
                <details>
                  <summary>
                    Instance states ({resource.instances.length})
                  </summary>
                  <ul>
                    {resource.instances.map((instance) => (
                      <li key={instance.instanceId}>
                        <code className={s.code}>{instance.instanceId}</code> ·{" "}
                        {instance.state}
                        {instance.serverId && (
                          <>
                            {" "}
                            ·{" "}
                            {servers.data?.find(
                              (server) => server.id === instance.serverId,
                            )?.name ?? instance.serverId}
                          </>
                        )}
                      </li>
                    ))}
                  </ul>
                </details>
              )}
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
      <section className={s.panel} aria-label="Log export">
        <h2>Export logs</h2>
        <p className={s.muted}>
          Download retained runtime samples and build output for this
          environment. These snapshots do not contain every log line. Runtime
          samples use their receipt time; build output uses the job creation
          time. Known secret values are redacted before capture, but application
          output may still contain private data.
        </p>
        <p className={s.muted}>
          Each export includes at most 100 entries and 64 KiB of log text,
          newest first. The file records the requested window and any further
          truncation. Inspect the host to refresh its runtime sample.
        </p>
        <ErrorNotice error={logExport.error} />
        <Label>
          Log export window
          <Select
            value={logHours}
            onChange={(event) => {
              setLogHours(Number(event.target.value) as 1 | 6 | 24);
              setLogNotice("");
            }}
          >
            <option value={1}>Last hour</option>
            <option value={6}>Last 6 hours</option>
            <option value={24}>Last 24 hours</option>
          </Select>
        </Label>
        <Button
          type="button"
          disabled={logExport.isPending}
          onClick={() => {
            setLogNotice("");
            logExport.run(async () => {
              const exported = await result(
                client.GET(`${root}/logs/export`, {
                  params: { path, query: { hours: logHours } },
                }),
              );
              download(
                `astilba-logs-${exported.environmentId}.json`,
                JSON.stringify(exported, null, 2),
                "application/json",
              );
              setLogNotice(
                exported.entries.length === 0
                  ? "Downloaded an empty export: no retained log snapshots in this window."
                  : exported.truncated
                    ? "Downloaded log snapshots. The export limit omitted or shortened some output."
                    : "Downloaded retained log snapshots.",
              );
            });
          }}
        >
          Download logs
        </Button>
        {logNotice && <p role="status">{logNotice}</p>}
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
                    <Button
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
                    </Button>
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
            <Label className={s.row}>
              <Checkbox name="copy" />
              Copy secret values into independent secrets (required for Compose
              sources with secret references; requires recent authentication)
            </Label>
            {revision.specification.compose &&
              !!revision.specification.routes?.length && (
                <p>
                  Source-defined routes must be changed explicitly. Create a
                  Compose environment and copy the bundle with new routes.
                </p>
              )}
            <Button
              type="submit"
              disabled={
                !!revision.specification.compose &&
                !!revision.specification.routes?.length
              }
            >
              Clone saved configuration
            </Button>
          </Form>
        )}
        <Button
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
        </Button>
        {bundle && (
          <>
            <pre className={s.log}>{bundle.readme}</pre>
            <div className={s.row}>
              <Anchor
                href={`/api/v1/workspaces/${workspaceId}/projects/${projectId}/environments/${environmentId}/export/archive`}
                download
              >
                Download complete bundle
              </Anchor>
              <Button
                type="button"
                onClick={() => download("compose.json", bundle.compose)}
              >
                Download Compose
              </Button>
              <Button
                type="button"
                onClick={() => download("routes.yaml", bundle.routes)}
              >
                Download routes
              </Button>
              <Button
                type="button"
                onClick={() => download("README.txt", bundle.readme)}
              >
                Download instructions
              </Button>
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
                  <Button
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
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      </details>
    </section>
  );
}
