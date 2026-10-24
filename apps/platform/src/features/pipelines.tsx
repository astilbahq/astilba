import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, client, result, type Schema } from "../api/client";
import * as s from "../styles";
import { ErrorNotice, Field, Form, text, useAction } from "../ui";
import { IntegrationSettings } from "./integrations";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;
const wsRoot = "/api/v1/workspaces/{workspaceId}" as const;
export function Pipelines({
  path,
  revision,
  dirty,
  write,
  owner,
  stage,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  dirty: boolean;
  write: boolean;
  owner: boolean;
  stage: (spec: Schema["Specification"]) => void;
}) {
  const action = useAction(),
    queries = useQueryClient();
  const workspace = { workspaceId: path.workspaceId };
  const [notice, setNotice] = useState("");
  const [connection, setConnection] = useState(""),
    [runner, setRunner] = useState("");
  const connections = useQuery({
    queryKey: ["git-connections", path.workspaceId],
    queryFn: () =>
      result(
        client.GET(`${wsRoot}/git-connections`, {
          params: { path: workspace },
        }),
      ),
  });
  const registries = useQuery({
    queryKey: ["registries", path.workspaceId],
    queryFn: () =>
      result(
        client.GET(`${wsRoot}/registries`, { params: { path: workspace } }),
      ),
  });
  const secrets = useQuery({
    queryKey: ["build-secrets", path.workspaceId],
    queryFn: () =>
      result(
        client.GET(`${wsRoot}/build-secrets`, { params: { path: workspace } }),
      ),
  });
  const servers = useQuery({
    queryKey: ["servers", path.workspaceId],
    queryFn: () =>
      result(client.GET(`${wsRoot}/servers`, { params: { path: workspace } })),
    refetchInterval: 15000,
  });
  const runs = useQuery({
    queryKey: ["pipeline-runs", path],
    queryFn: () => result(client.GET(`${root}/builds`, { params: { path } })),
    refetchInterval: 2000,
  });
  const artifacts = useQuery({
    queryKey: ["build-artifacts", path.workspaceId],
    queryFn: () =>
      result(
        client.GET(`${wsRoot}/artifacts`, { params: { path: workspace } }),
      ),
    refetchInterval: 5000,
  });
  const source = useQuery({
    queryKey: ["git-source", path],
    enabled: !!revision.specification.compose,
    queryFn: async () => {
      try {
        return await result(client.GET(`${root}/source`, { params: { path } }));
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
    refetchInterval: 3000,
  });
  const refresh = async () => {
    await queries.invalidateQueries({ queryKey: ["pipeline-runs", path] });
    await queries.invalidateQueries({ queryKey: ["git-source", path] });
  };
  const selected = connections.data?.find((c) => c.id === connection);
  const runnerFields = (
    <>
      <label>
        Build runner
        <select
          name="runner"
          value={runner}
          onChange={(e) => setRunner(e.target.value)}
          required
        >
          <option value="">Choose a runner</option>
          {servers.data
            ?.filter((v) => v.role !== "deployment" && v.capabilities?.build)
            .map((v) => (
              <option key={v.id} value={v.id}>
                {v.name} · {v.status} · {v.capabilities?.arch}
              </option>
            ))}
        </select>
      </label>
      <label>
        Fallback runner
        <select name="fallback" defaultValue="">
          <option value="">Queue on selected runner</option>
          {servers.data
            ?.filter(
              (v) =>
                v.id !== runner &&
                v.role !== "deployment" &&
                v.capabilities?.build,
            )
            .map((v) => (
              <option key={v.id} value={v.id}>
                {v.name} · {v.status}
              </option>
            ))}
        </select>
      </label>
    </>
  );
  const repositoryFields = (
    <>
      <label>
        Repository connection
        <select
          name="connection"
          required
          value={connection}
          onChange={(e) => setConnection(e.target.value)}
        >
          <option value="">Choose a repository</option>
          {connections.data?.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name} · {c.repository}
            </option>
          ))}
        </select>
      </label>
      <Field name="branch" label="Branch" />
      {runnerFields}
    </>
  );
  return (
    <section className={s.stack} aria-label="Git and build workflows">
      <section className={s.panel}>
        <h2>Git and builds</h2>
        <p className={s.muted}>
          Fetch an exact commit on your runner, build with a Dockerfile, then
          promote its immutable image. Offline or busy runners queue work. A
          fallback runs only when you explicitly choose one.
        </p>
        <ErrorNotice
          error={
            action.error ??
            connections.error ??
            servers.error ??
            runs.error ??
            source.error ??
            registries.error ??
            artifacts.error ??
            secrets.error
          }
        />
        {notice && <p role="status">{notice}</p>}
        {dirty && (
          <p className={s.notice}>
            Save your configuration before importing, submitting or building.
            Your unsaved edits stay in the editor.
          </p>
        )}
        {!revision.specification.compose && write && (
          <details>
            <summary>Add a service from Git</summary>
            <Form
              action={action}
              label="Add Git service"
              onSubmit={(d) =>
                action.run(async () => {
                  if (dirty) throw new Error("Save the current draft first.");
                  if (!selected)
                    throw new Error("Choose a repository connection.");
                  stage({
                    ...revision.specification,
                    applications: [
                      ...revision.specification.applications,
                      {
                        id: crypto.randomUUID(),
                        name: text(d, "name"),
                        kind: "web",
                        source: {
                          kind: "git",
                          provider: selected.provider,
                          repository:
                            (selected.provider === "github"
                              ? "https://github.com"
                              : selected.baseUrl) +
                            "/" +
                            selected.repository,
                          ref: text(d, "branch"),
                        },
                      },
                    ],
                  });
                  setNotice(
                    "Git service staged. Save the revision, then choose the service in the build form.",
                  );
                })
              }
            >
              <Field name="name" label="Service name" />
              {repositoryFields}
              <button type="submit" disabled={dirty || action.isPending}>
                Stage Git service
              </button>
            </Form>
          </details>
        )}
        {revision.specification.compose &&
          (source.data ? (
            <div className={s.stack}>
              <p>
                Source: {source.data.status} ·{" "}
                {source.data.configuration.branch} ·{" "}
                <code>{source.data.importedCommit.slice(0, 12)}</code>
              </p>
              {owner && (
                <button
                  type="button"
                  disabled={action.isPending}
                  onClick={() =>
                    action.run(async () => {
                      await result(
                        client.PUT(`${root}/source/policy`, {
                          params: { path },
                          body: {
                            autoDeploy: !source.data?.configuration.autoDeploy,
                          },
                        }),
                      );
                      await refresh();
                    })
                  }
                >
                  {source.data.configuration.autoDeploy
                    ? "Disable automatic source deployments"
                    : "Enable automatic source deployments"}
                </button>
              )}
              {source.data.pendingCommit && (
                <p className={s.notice}>
                  The branch changed while your draft was pending. Compare the
                  source and your draft, then explicitly refresh to replace the
                  draft.
                </p>
              )}
              {write && (
                <Form
                  action={action}
                  label="Refresh Git source"
                  onSubmit={(d) =>
                    action.run(async () => {
                      await result(
                        client.POST(`${root}/source/refresh`, {
                          params: { path },
                          body: {
                            baseRevisionId: revision.id,
                            discardDraft: d.has("discard"),
                          },
                        }),
                      );
                      setNotice(
                        "Import queued. When it succeeds, use Reload current to load the imported revision.",
                      );
                      await refresh();
                    })
                  }
                >
                  <label>
                    <input type="checkbox" name="discard" />
                    Replace the saved draft with the branch's current source
                  </label>
                  <button type="submit" disabled={dirty || action.isPending}>
                    Refresh from Git
                  </button>
                </Form>
              )}
              {write && (
                <button
                  type="button"
                  disabled={dirty || action.isPending || source.data.clean}
                  onClick={() =>
                    action.run(async () => {
                      await result(
                        client.POST(`${root}/source/submit`, {
                          params: { path },
                          body: {
                            baseRevisionId: revision.id,
                            requestKey: crypto.randomUUID(),
                          },
                        }),
                      );
                      setNotice(
                        "Submission queued. Merge the resulting review in your Git provider before deploying.",
                      );
                      await refresh();
                    })
                  }
                >
                  Submit saved draft for review
                </button>
              )}
            </div>
          ) : (
            owner && (
              <details>
                <summary>Import Compose from Git</summary>
                <Form
                  action={action}
                  label="Import Compose from Git"
                  onSubmit={(d) =>
                    action.run(async () => {
                      await result(
                        client.PUT(`${root}/source`, {
                          params: { path },
                          body: {
                            baseRevisionId: revision.id,
                            configuration: {
                              connectionId: connection,
                              branch: text(d, "branch"),
                              runnerId: runner,
                              fallbackRunnerId:
                                text(d, "fallback") || undefined,
                              entrypoints: text(d, "entrypoints")
                                .split("\n")
                                .filter(Boolean),
                              paths: text(d, "paths")
                                .split("\n")
                                .filter(Boolean),
                              autoDeploy: d.has("autoDeploy"),
                            },
                          },
                        }),
                      );
                      setNotice(
                        "Import queued. Configure the push webhook shown when creating the connection. Use Reload current after import.",
                      );
                      await refresh();
                    })
                  }
                >
                  {repositoryFields}
                  <label>
                    Compose entrypoints, one path per line
                    <textarea
                      name="entrypoints"
                      required
                      defaultValue="compose.yaml"
                    />
                  </label>
                  <label>
                    Selected source files, one path per line
                    <textarea
                      name="paths"
                      required
                      defaultValue="compose.yaml"
                    />
                  </label>
                  <label>
                    <input type="checkbox" name="autoDeploy" />
                    Automatically deploy future imported commits after the first
                    reviewed deployment
                  </label>
                  <button type="submit" disabled={dirty || action.isPending}>
                    Import source
                  </button>
                </Form>
              </details>
            )
          ))}
        {write && (
          <details>
            <summary>Build a service</summary>
            <Form
              action={action}
              label="Build a service"
              onSubmit={(d) =>
                action.run(async () => {
                  const argumentsMap: Record<string, string> = {};
                  for (const line of text(d, "arguments")
                    .split("\n")
                    .filter(Boolean)) {
                    const index = line.indexOf("=");
                    if (index < 1)
                      throw new Error(
                        "Build arguments need NAME=value, one per line.",
                      );
                    argumentsMap[line.slice(0, index)] = line.slice(index + 1);
                  }
                  const secretMap: Record<string, string> = {};
                  const secret = text(d, "secret");
                  if (secret) {
                    const name = text(d, "secretName");
                    if (!name)
                      throw new Error("Give the build secret a mount name.");
                    secretMap[name] = secret;
                  }
                  await result(
                    client.POST(`${root}/builds`, {
                      params: { path },
                      body: {
                        baseRevisionId: revision.id,
                        applicationId: text(d, "app"),
                        requestKey: crypto.randomUUID(),
                        configuration: {
                          connectionId: connection,
                          branch: text(d, "branch"),
                          runnerId: runner,
                          fallbackRunnerId: text(d, "fallback") || undefined,
                          registryId: text(d, "registry") || undefined,
                          context: text(d, "context"),
                          dockerfile: text(d, "dockerfile"),
                          architecture: text(d, "architecture") as
                            | "amd64"
                            | "arm64",
                          arguments: argumentsMap,
                          secrets: secretMap,
                          autoBuild: d.has("autoBuild"),
                          autoDeploy: d.has("autoDeploy"),
                        },
                      },
                    }),
                  );
                  setNotice(
                    "Build queued. Existing workloads stay on their applied image until deployment.",
                  );
                  await refresh();
                })
              }
            >
              <label>
                Service
                <select name="app" required>
                  {revision.specification.applications
                    .filter((a) => a.kind !== "postgres")
                    .map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}
                      </option>
                    ))}
                </select>
              </label>
              {repositoryFields}
              <label>
                Build context
                <input name="context" defaultValue="." required />
              </label>
              <label>
                Dockerfile, relative to context
                <input name="dockerfile" defaultValue="Dockerfile" required />
              </label>
              <label>
                Architecture
                <select name="architecture">
                  <option value="arm64">Linux arm64</option>
                  <option value="amd64">Linux amd64</option>
                </select>
              </label>
              <label>
                Registry destination
                <select name="registry">
                  <option value="">Keep image on this runner</option>
                  {registries.data?.map((r) => (
                    <option key={r.id} value={r.id}>
                      {r.name} · {r.repository}
                    </option>
                  ))}
                </select>
              </label>
              <p className={s.muted}>
                A local image can deploy only on the runner that built it.
                Registry images can be promoted to another environment with
                matching architecture.
              </p>
              <label>
                Plain build arguments
                <textarea
                  name="arguments"
                  placeholder="PUBLIC_BASE_URL=https://example.com"
                />
              </label>
              <label>
                Build secret
                <select name="secret">
                  <option value="">None</option>
                  {secrets.data?.map((v) => (
                    <option key={v.id} value={v.id}>
                      {v.name} · {new Date(v.createdAt).toLocaleString()}
                    </option>
                  ))}
                </select>
              </label>
              <Field
                name="secretName"
                label="Secret mount name"
                required={false}
              />
              <p className={s.muted}>
                Build secrets are separate from runtime secrets and available
                through Dockerfile secret mounts. Your Dockerfile must avoid
                copying or printing them.
              </p>
              <label>
                <input type="checkbox" name="autoBuild" />
                Build new branch commits from authenticated push webhooks
              </label>
              {owner && (
                <label>
                  <input type="checkbox" name="autoDeploy" />
                  Promote successful builds and automatically deploy after the
                  first reviewed deployment
                </label>
              )}
              <button type="submit" disabled={dirty || action.isPending}>
                Queue build
              </button>
            </Form>
          </details>
        )}
      </section>
      {owner && <IntegrationSettings workspaceId={path.workspaceId} />}
      {write && !!artifacts.data?.length && (
        <section className={s.panel}>
          <h3>Promote an existing artifact</h3>
          <p className={s.muted}>
            Reuse an image built in any environment in this workspace. This
            changes the saved draft and preserves the destination's runtime
            variables and bindings.
          </p>
          <Form
            action={action}
            label="Promote existing artifact"
            onSubmit={(d) =>
              action.run(async () => {
                const artifact = artifacts.data?.find(
                  (a) => a.id === text(d, "artifact"),
                );
                if (!artifact) throw new Error("Choose an artifact.");
                await result(
                  client.POST(`${root}/artifacts/{artifactId}/promote`, {
                    params: { path: { ...path, artifactId: artifact.id } },
                    body: {
                      applicationId: text(d, "app"),
                      baseRevisionId: revision.id,
                      inputHash: artifact.inputHash,
                    },
                  }),
                );
                setNotice(
                  "Artifact promoted. Use Reload current, then review a deployment plan.",
                );
              })
            }
          >
            <label>
              Built artifact
              <select name="artifact" required>
                {artifacts.data.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.commit.slice(0, 12)} · {a.architecture} · {a.image}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Destination service
              <select name="app" required>
                {revision.specification.applications
                  .filter((a) => a.kind !== "postgres")
                  .map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}
                    </option>
                  ))}
              </select>
            </label>
            <button type="submit" disabled={dirty || action.isPending}>
              Promote existing artifact to draft
            </button>
          </Form>
        </section>
      )}
      <section className={s.panel}>
        <h3>Source and build jobs</h3>
        {!runs.data?.length && (
          <p className={s.muted}>No source or build jobs yet.</p>
        )}
        {runs.data?.map((run) => {
          const report = run.result as
            | { logs?: string; cache?: string; reviewUrl?: string }
            | undefined;
          return (
            <article key={run.id} className={s.stack}>
              <h4>
                {run.action} · {run.commit.slice(0, 12)} · {run.operation.state}
              </h4>
              <p>{run.operation.note}</p>
              {run.automaticDeployment && (
                <p>
                  Automatic deployment: {run.automaticDeployment.state}.{" "}
                  {run.automaticDeployment.note}
                </p>
              )}
              <p className={s.muted}>
                Runner:{" "}
                {servers.data?.find((v) => v.id === run.operation.serverId)
                  ?.name ?? run.operation.serverId}
              </p>
              {!!run.operation.steps.length && (
                <ol>
                  {run.operation.steps.map((step) => (
                    <li key={step.name}>
                      {step.name} · {step.state} {step.detail}
                    </li>
                  ))}
                </ol>
              )}
              {report?.cache && <p>{report.cache}</p>}
              {report?.reviewUrl && (
                <a href={report.reviewUrl} target="_blank" rel="noreferrer">
                  Open source review
                </a>
              )}
              {report?.logs && (
                <details>
                  <summary>Build output</summary>
                  <pre
                    className={s.code}
                    style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}
                  >
                    {report.logs}
                  </pre>
                </details>
              )}
              {write &&
                !["succeeded", "failed", "cancelled"].includes(
                  run.operation.state,
                ) && (
                  <button
                    type="button"
                    disabled={action.isPending}
                    onClick={() =>
                      action.run(async () => {
                        await result(
                          client.POST(
                            `${wsRoot}/operations/{operationId}/cancel`,
                            {
                              params: {
                                path: { ...workspace, operationId: run.id },
                              },
                            },
                          ),
                        );
                        await refresh();
                      })
                    }
                  >
                    Cancel {run.action}
                  </button>
                )}
              {run.artifact && (
                <div>
                  <p>
                    Artifact:{" "}
                    <code className={s.code}>{run.artifact.digest}</code> ·{" "}
                    {run.artifact.architecture}
                  </p>
                  {write && (
                    <Form
                      action={action}
                      label="Promote artifact"
                      onSubmit={(d) =>
                        action.run(async () => {
                          if (!run.artifact) return;
                          await result(
                            client.POST(
                              `${root}/artifacts/{artifactId}/promote`,
                              {
                                params: {
                                  path: {
                                    ...path,
                                    artifactId: run.artifact.id,
                                  },
                                },
                                body: {
                                  applicationId: text(d, "app"),
                                  baseRevisionId: revision.id,
                                  inputHash: run.artifact.inputHash,
                                },
                              },
                            ),
                          );
                          setNotice(
                            "Artifact promoted to the saved draft. Use Reload current, then review a deployment plan. Runtime variables and bindings are preserved.",
                          );
                        })
                      }
                    >
                      <label>
                        Destination service
                        <select name="app">
                          {revision.specification.applications
                            .filter((a) => a.kind !== "postgres")
                            .map((a) => (
                              <option key={a.id} value={a.id}>
                                {a.name}
                              </option>
                            ))}
                        </select>
                      </label>
                      <button
                        type="submit"
                        disabled={dirty || action.isPending}
                      >
                        Promote artifact to draft
                      </button>
                    </Form>
                  )}
                </div>
              )}
            </article>
          );
        })}
      </section>
    </section>
  );
}
