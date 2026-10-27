import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, client, result, type Schema } from "../api/client";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Label } from "../components/label";
import { Link } from "../components/link";
import { Select } from "../components/select";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "../ui";
import * as previewStyles from "./previews.styles";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;
export function Previews({
  path,
  revision,
  dirty,
  owner,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  dirty: boolean;
  owner: boolean;
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
  const previews = useQuery({
    queryKey: ["previews", path],
    queryFn: () => result(client.GET(`${root}/previews`, { params: { path } })),
    refetchInterval: 5000,
  });
  const refresh = async () => {
    await query.invalidateQueries({ queryKey: ["previews", path] });
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
        database password. Template edits take effect only after an owner saves
        a new policy.
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
              {owner && (
                <div className={s.row}>
                  {p.state === "approval_required" && (
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
                  <Button
                    type="button"
                    disabled={action.isPending}
                    onClick={() => run(p, "refresh")}
                  >
                    {p.state === "deleted"
                      ? "Recreate preview"
                      : "Refresh preview"}
                  </Button>
                  {p.state !== "deleted" && p.state !== "deleting" && (
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
          No previews yet. Astilba discovers open requests and listens for
          signed provider events. Fork commits wait for owner approval.
        </p>
      )}
    </section>
  );
}

function PreviewPolicyEditor({
  path,
  revision,
  policy,
  disabled,
  refresh,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  policy?: Schema["PreviewPolicy"];
  disabled: boolean;
  refresh: () => Promise<void>;
}) {
  const action = useAction();
  const cfg = policy?.configuration;
  const [builder, setBuilder] = useState<"dockerfile" | "railpack">(
    cfg?.build.builder ?? "dockerfile",
  );
  const connections = useQuery({
    queryKey: ["git-connections", path.workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/git-connections", {
          params: { path },
        }),
      ),
  });
  const servers = useQuery({
    queryKey: ["servers", path.workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/servers", {
          params: { path },
        }),
      ),
    refetchInterval: 15000,
  });
  const hosts = servers.data?.filter(
    (s) =>
      s.previewOnly &&
      s.role === "combined" &&
      s.status !== "revoked" &&
      !s.transferRequired &&
      s.capabilities?.previews,
  );
  const budgets = cfg?.build.budget;
  return (
    <details>
      <summary>
        {policy ? "Preview policy settings" : "Set up automatic previews"}
      </summary>
      <p>
        Use a fresh preview-only server with no production workloads or private
        production network access. Docker containers share the host kernel. Fork
        approval accepts that commit’s code execution and access to preview
        secrets.
      </p>
      <p>
        This increment supports one stateless Git-built service plus
        pinned-image services, including PostgreSQL. Compose previews, registry
        publication and build-time secrets are unavailable. Configure a
        connection under Integrations first.
      </p>
      <ErrorNotice error={connections.error || servers.error || action.error} />
      {!hosts?.length && (
        <p>
          Connect a preview-only server on Servers and install an agent that
          supports previews.
        </p>
      )}
      <p>
        Enabling, approving or removing previews requires recent identity
        confirmation.{" "}
        <Link
          to="/account"
          search={{ workspace: path.workspaceId }}
          target="_blank"
          rel="noreferrer"
        >
          Open Account security
        </Link>{" "}
        to confirm, then return and retry.
      </p>
      <Form
        action={action}
        label="Preview policy"
        onSubmit={(d) =>
          action.run(async () => {
            const body: Schema["SavePreviewPolicy"] = {
              enabled: true,
              generation: policy?.generation ?? 0,
              configuration: {
                baseRevisionId: revision.id,
                applicationId: text(d, "applicationId"),
                build: {
                  builder,
                  connectionId: text(d, "connectionId"),
                  branch: text(d, "branch"),
                  runnerId: text(d, "runnerId"),
                  context: text(d, "context"),
                  dockerfile:
                    builder === "dockerfile"
                      ? text(d, "dockerfile")
                      : "Dockerfile",
                  architecture: text(d, "architecture") as "arm64" | "amd64",
                  arguments: {},
                  secrets: {},
                  autoBuild: false,
                  autoDeploy: false,
                  budget: {
                    cpus: Number(text(d, "buildCpus")),
                    memoryMiB: Number(text(d, "buildMemory")),
                    sourceMiB: Number(text(d, "buildSource")),
                    stateMiB: Number(text(d, "buildState")),
                    minutes: Number(text(d, "buildMinutes")),
                  },
                },
                maxActive: Number(text(d, "maxActive")),
                maxConcurrentBuilds: Number(text(d, "maxBuilds")),
                limits: {
                  cpus: Number(text(d, "cpus")),
                  memoryMiB: Number(text(d, "memory")),
                  pids: Number(text(d, "pids")),
                },
                closeGraceHours: Number(text(d, "grace")),
                generatedDomains: d.get("domains") === "on",
              },
            };
            await result(
              client.PUT(`${root}/preview-policy`, { params: { path }, body }),
            );
            await refresh();
          })
        }
      >
        <fieldset
          disabled={disabled || action.isPending}
          className={previewStyles.fields}
        >
          <legend>Source and host</legend>
          <Label>
            Git-built service
            <Select
              name="applicationId"
              defaultValue={cfg?.applicationId}
              required
            >
              <option value="">Choose a stateless service</option>
              {revision.specification.applications
                .filter((a) => a.kind !== "postgres" && !a.mounts?.length)
                .map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
            </Select>
          </Label>
          <Label>
            Git connection
            <Select
              name="connectionId"
              defaultValue={cfg?.build.connectionId}
              required
            >
              <option value="">Choose a repository</option>
              {connections.data?.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name} · {c.repository}
                </option>
              ))}
            </Select>
          </Label>
          <Field
            name="branch"
            label="Target branch"
            defaultValue={cfg?.build.branch ?? "main"}
          />
          <Label>
            Preview-only server
            <Select name="runnerId" defaultValue={cfg?.build.runnerId} required>
              <option value="">Choose a reserved host</option>
              {hosts?.map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name} · {h.status}
                </option>
              ))}
            </Select>
          </Label>
          <Label>
            Architecture
            <Select
              name="architecture"
              defaultValue={cfg?.build.architecture ?? "arm64"}
            >
              <option value="arm64">arm64</option>
              <option value="amd64">amd64</option>
            </Select>
          </Label>
          <Label>
            Builder
            <Select
              value={builder}
              onChange={(e) =>
                setBuilder(e.target.value as "dockerfile" | "railpack")
              }
            >
              <option value="dockerfile">Dockerfile</option>
              <option value="railpack">Railpack</option>
            </Select>
          </Label>
          <Field
            name="context"
            label="Build context"
            defaultValue={cfg?.build.context ?? "."}
          />
          {builder === "dockerfile" && (
            <Field
              name="dockerfile"
              label="Dockerfile"
              defaultValue={cfg?.build.dockerfile ?? "Dockerfile"}
            />
          )}
        </fieldset>
        <fieldset
          disabled={disabled || action.isPending}
          className={previewStyles.fields}
        >
          <legend>Resource and cleanup limits</legend>
          <Field
            name="maxActive"
            label="Maximum active previews"
            type="number"
            min={1}
            max={10}
            defaultValue={cfg?.maxActive ?? 3}
          />
          <Field
            name="maxBuilds"
            label="Maximum concurrent builds"
            type="number"
            min={1}
            max={3}
            defaultValue={cfg?.maxConcurrentBuilds ?? 1}
          />
          <Field
            name="cpus"
            label="CPU cores per service"
            type="number"
            min={1}
            max={4}
            defaultValue={cfg?.limits.cpus ?? 1}
          />
          <Field
            name="memory"
            label="Memory per service (MiB)"
            type="number"
            min={128}
            max={8192}
            defaultValue={cfg?.limits.memoryMiB ?? 512}
          />
          <Field
            name="pids"
            label="Processes per service"
            type="number"
            min={32}
            max={512}
            defaultValue={cfg?.limits.pids ?? 128}
          />
          <Field
            name="grace"
            label="Hours to retain a closed preview"
            type="number"
            min={0}
            max={168}
            defaultValue={cfg?.closeGraceHours ?? 24}
          />
          <p>
            After this delay, Astilba removes the preview’s services and owned
            volumes. Zero deletes immediately. Runtime disk volumes have no byte
            quota; monitor host capacity.
          </p>
          <Label>
            <Checkbox
              name="domains"
              defaultChecked={cfg?.generatedDomains ?? false}
            />
            Publish generated HTTPS names from this template’s HTTP routes
          </Label>
          <p>
            Generated names require completed application-domain setup for the
            selected host. Traffic goes directly to that host.
          </p>
        </fieldset>
        <details>
          <summary>Build resource limits</summary>
          <p>
            Memory must cover source, builder storage and tools. Railpack needs
            at least 6144 MiB memory, 1024 MiB source and 4096 MiB builder
            storage.
          </p>
          <fieldset
            disabled={disabled || action.isPending}
            className={previewStyles.fields}
          >
            <legend>Build budget</legend>
            <Field
              name="buildCpus"
              label="Build CPU cores"
              type="number"
              min={1}
              max={8}
              defaultValue={budgets?.cpus ?? 2}
            />
            <Field
              name="buildMemory"
              label="Build memory (MiB)"
              type="number"
              min={512}
              max={8192}
              defaultValue={budgets?.memoryMiB ?? 6144}
            />
            <Field
              name="buildSource"
              label="Source storage (MiB)"
              type="number"
              min={64}
              max={1024}
              defaultValue={budgets?.sourceMiB ?? 1024}
            />
            <Field
              name="buildState"
              label="Builder storage (MiB)"
              type="number"
              min={128}
              max={6144}
              defaultValue={budgets?.stateMiB ?? 4096}
            />
            <Field
              name="buildMinutes"
              label="Maximum build minutes"
              type="number"
              min={1}
              max={30}
              defaultValue={budgets?.minutes ?? 30}
            />
          </fieldset>
        </details>
        <Label>
          <Checkbox
            name="accept"
            required
            disabled={disabled || action.isPending}
          />
          I approve automatic code execution, publication of configured preview
          routes, access to preview-only secrets and deletion after the
          configured grace period.
        </Label>
        {disabled && (
          <p>
            Save the template and clean up active previews before changing this
            policy.
          </p>
        )}
        <BusyButton
          busy={action.isPending}
          disabled={disabled || !hosts?.length}
        >
          Enable automatic previews
        </BusyButton>
      </Form>
    </details>
  );
}
