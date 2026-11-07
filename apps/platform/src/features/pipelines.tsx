import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, client, result, type Schema } from "../api/client";
import { publishDraft, type EnvironmentPath } from "../api/environment";
import { gitConnectionsOptions } from "../api/integrations";
import { serversOptions } from "../api/servers";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Label } from "../components/label";
import { Select } from "../components/select";
import { Textarea } from "../components/textarea";
import * as s from "../styles";
import { ErrorNotice, Field, Form, text, useAction } from "../ui";
import { IntegrationSettings } from "./integrations";
import { BuildServiceForm } from "./pipelines/build-service-form";
import { PipelineJobs } from "./pipelines/pipeline-jobs";

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
  const connections = useQuery(gitConnectionsOptions(path.workspaceId));
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
    ...serversOptions(path.workspaceId),
    refetchInterval: 15000,
  });
  const runs = useQuery({
    queryKey: ["pipeline-runs", path],
    queryFn: () => result(client.GET(`${root}/builds`, { params: { path } })),
    refetchInterval: 2000,
  });
  const rejections = useQuery({
    queryKey: ["build-rejections", path],
    queryFn: () =>
      result(client.GET(`${root}/build-rejections`, { params: { path } })),
    refetchInterval: 3000,
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
    await queries.invalidateQueries({ queryKey: ["build-rejections", path] });
    await queries.invalidateQueries({ queryKey: ["git-source", path] });
  };
  const promoteArtifact = async (
    artifact: Schema["BuildArtifact"],
    applicationId: string,
  ) => {
    const next = await result(
      client.POST(`${root}/artifacts/{artifactId}/promote`, {
        params: { path: { ...path, artifactId: artifact.id } },
        body: {
          applicationId,
          baseRevisionId: revision.id,
          inputHash: artifact.inputHash,
        },
      }),
    );
    await publishDraft(queries, path, next, revision.id);
  };
  const selected = connections.data?.find((c) => c.id === connection);
  const runnerFields = (
    <>
      <Label>
        Build runner
        <Select
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
        </Select>
      </Label>
      <Label>
        Fallback runner
        <Select name="fallback" defaultValue="">
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
        </Select>
      </Label>
    </>
  );
  const repositoryFields = (
    <>
      <Label>
        Repository connection
        <Select
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
        </Select>
      </Label>
      <Field name="branch" label="Branch" />
      {runnerFields}
    </>
  );
  return (
    <section className={s.stack} aria-label="Git and build workflows">
      <section className={s.panel}>
        <h2>Git and builds</h2>
        <p className={s.muted}>
          Fetch an exact commit on your runner, build with Dockerfile or
          Railpack, then promote its immutable image. Offline or busy runners
          queue work. A fallback runs only when you explicitly choose one.
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
              <Button type="submit" disabled={dirty || action.isPending}>
                Stage Git service
              </Button>
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
                <Button
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
                </Button>
              )}
              {source.data.pendingCommit && (
                <p className={s.notice}>
                  {source.data.status === "conflict"
                    ? "The branch changed while your draft was pending. Compare the source and your draft, then explicitly refresh to replace the draft."
                    : `Commit ${source.data.pendingCommit.slice(0, 12)} has not been imported. Refresh the source to try now.`}
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
                  <Label>
                    <Checkbox name="discard" />
                    Replace the saved draft with the branch's current source
                  </Label>
                  <Button type="submit" disabled={dirty || action.isPending}>
                    Refresh from Git
                  </Button>
                </Form>
              )}
              {write && (
                <Button
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
                </Button>
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
                  <Label>
                    Compose entrypoints, one path per line
                    <Textarea
                      name="entrypoints"
                      required
                      defaultValue="compose.yaml"
                    />
                  </Label>
                  <Label>
                    Selected source files, one path per line
                    <Textarea
                      name="paths"
                      required
                      defaultValue="compose.yaml"
                    />
                  </Label>
                  <Label>
                    <Checkbox name="autoDeploy" />
                    Automatically deploy future imported commits after the first
                    reviewed deployment
                  </Label>
                  <Button type="submit" disabled={dirty || action.isPending}>
                    Import source
                  </Button>
                </Form>
              </details>
            )
          ))}
        {/* Keep controlled build drafts mounted when write access changes. */}
        <BuildServiceForm
          path={path}
          revision={revision}
          write={write}
          owner={owner}
          dirty={dirty}
          action={action}
          connection={connection}
          runner={runner}
          repositoryFields={repositoryFields}
          servers={servers.data}
          registries={registries.data}
          secrets={secrets.data}
          onQueued={async () => {
            setNotice(
              "Build queued. Existing workloads stay on their applied image until deployment.",
            );
            await refresh();
          }}
        />
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
                await promoteArtifact(artifact, text(d, "app"));
                setNotice(
                  "Artifact promoted. Use Reload current, then review a deployment plan.",
                );
              })
            }
          >
            <Label>
              Built artifact
              <Select name="artifact" required>
                {artifacts.data.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.commit.slice(0, 12)} · {a.architecture} · {a.image}
                  </option>
                ))}
              </Select>
            </Label>
            <Label>
              Destination service
              <Select name="app" required>
                {revision.specification.applications
                  .filter((a) => a.kind !== "postgres")
                  .map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}
                    </option>
                  ))}
              </Select>
            </Label>
            <Button type="submit" disabled={dirty || action.isPending}>
              Promote existing artifact to draft
            </Button>
          </Form>
        </section>
      )}
      <PipelineJobs
        applications={revision.specification.applications}
        runs={runs.data}
        rejections={rejections.data}
        rejectionsError={rejections.error}
        servers={servers.data}
        write={write}
        dirty={dirty}
        action={action}
        onCancel={(operationId) =>
          action.run(async () => {
            await result(
              client.POST(`${wsRoot}/operations/{operationId}/cancel`, {
                params: { path: { ...workspace, operationId } },
              }),
            );
            await refresh();
          })
        }
        onPromote={(artifact, applicationId) =>
          action.run(async () => {
            if (!artifact) return;
            await promoteArtifact(artifact, applicationId);
            setNotice(
              "Artifact promoted to the saved draft. Use Reload current, then review a deployment plan. Runtime variables and bindings are preserved.",
            );
          })
        }
      />
    </section>
  );
}
