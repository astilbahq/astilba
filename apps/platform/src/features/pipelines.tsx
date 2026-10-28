import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { ApiError, client, result, type Schema } from "../api/client";
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
import { IntegrationSettings } from "./integrations";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;
const wsRoot = "/api/v1/workspaces/{workspaceId}" as const;
const buildDefaults = {
  dockerfile: {
    cpus: "2",
    memoryMiB: "2048",
    sourceMiB: "256",
    stateMiB: "1024",
    minutes: "30",
  },
  railpack: {
    cpus: "2",
    memoryMiB: "6144",
    sourceMiB: "1024",
    stateMiB: "4096",
    minutes: "30",
  },
};
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
  const controlsId = useId();
  const [builder, setBuilder] = useState<"dockerfile" | "railpack">(
    "dockerfile",
  );
  const [dockerfile, setDockerfile] = useState("Dockerfile");
  const [buildCommand, setBuildCommand] = useState("");
  const [startCommand, setStartCommand] = useState("");
  const [limits, setLimits] = useState(buildDefaults.dockerfile);
  const buildFieldError = (...fields: string[]) =>
    action.error instanceof ApiError &&
    fields.some(
      (field) =>
        action.error instanceof ApiError &&
        action.error.detail.fields?.includes(`build.${field}`),
    );
  const builderError = buildFieldError("builder", "railpack");
  const inputsError = buildFieldError("arguments", "secrets");
  const budgetError =
    action.error instanceof ApiError &&
    action.error.detail.fields?.includes("build.budget");
  const watchError =
    action.error instanceof ApiError &&
    action.error.detail.fields?.includes("build.watchPaths");
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
                    if (!name) throw new Error("Give the build secret a name.");
                    secretMap[name] = secret;
                  }
                  const budget = {
                    cpus: Number(text(d, "cpus")),
                    memoryMiB: Number(text(d, "memoryMiB")),
                    sourceMiB: Number(text(d, "sourceMiB")),
                    stateMiB: Number(text(d, "stateMiB")),
                    minutes: Number(text(d, "minutes")),
                  };
                  const customBudget =
                    budget.cpus !== 2 ||
                    budget.memoryMiB !== 2048 ||
                    budget.sourceMiB !== 256 ||
                    budget.stateMiB !== 1024 ||
                    budget.minutes !== 30;
                  const watchPaths = text(d, "watchPaths")
                    .split("\n")
                    .map((p) => p.trim())
                    .filter(Boolean);
                  const watchMessage =
                    watchPaths.length > 16
                      ? "Use at most 16 watch paths."
                      : new Set(watchPaths).size !== watchPaths.length
                        ? "Remove duplicate watch paths."
                        : watchPaths.some((p) => Array.from(p).length > 240)
                          ? "Use watch paths of at most 240 characters."
                          : undefined;
                  if (watchMessage)
                    throw new ApiError(
                      {
                        code: "invalid_input",
                        message: watchMessage,
                        fields: ["build.watchPaths"],
                        requestId: "",
                      },
                      400,
                    );
                  if (customBudget || watchPaths.length) {
                    for (const id of [runner, text(d, "fallback")].filter(
                      Boolean,
                    )) {
                      if (
                        !servers.data?.find((server) => server.id === id)
                          ?.capabilities?.buildControls
                      ) {
                        throw new Error(
                          "Update the selected runner and fallback agents before using custom build limits or watch paths.",
                        );
                      }
                    }
                  }
                  if (builder === "railpack") {
                    for (const id of [runner, text(d, "fallback")].filter(
                      Boolean,
                    )) {
                      if (
                        servers.data?.find((server) => server.id === id)
                          ?.capabilities?.railpackVersion !== "0.43.0"
                      ) {
                        throw new ApiError(
                          {
                            code: "invalid_input",
                            message:
                              "Update the selected runner and fallback to support Railpack 0.43.0 before queuing this build.",
                            fields: ["build.builder"],
                            requestId: "",
                          },
                          400,
                        );
                      }
                    }
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
                          builder:
                            builder === "railpack" ? "railpack" : undefined,
                          dockerfile:
                            builder === "dockerfile" ? dockerfile : undefined,
                          railpack:
                            builder === "railpack" &&
                            (buildCommand || startCommand)
                              ? { buildCommand, startCommand }
                              : undefined,
                          architecture: text(d, "architecture") as
                            | "amd64"
                            | "arm64",
                          arguments: argumentsMap,
                          secrets: secretMap,
                          budget: customBudget ? budget : undefined,
                          watchPaths: watchPaths.length
                            ? watchPaths
                            : undefined,
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
              <Label>
                Service
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
              {repositoryFields}
              <Label>
                Build context
                <Input name="context" defaultValue="." required />
              </Label>
              <Label>
                Builder
                <Select
                  name="builder"
                  value={builder}
                  aria-invalid={builderError || undefined}
                  aria-describedby={`${controlsId}-builder`}
                  onChange={(e) => {
                    const next = e.target.value as typeof builder;
                    if (
                      Object.keys(limits).every(
                        (key) =>
                          limits[key as keyof typeof limits] ===
                          buildDefaults[builder][key as keyof typeof limits],
                      )
                    )
                      setLimits(buildDefaults[next]);
                    setBuilder(next);
                  }}
                >
                  <option value="dockerfile">Dockerfile</option>
                  <option value="railpack">Railpack</option>
                </Select>
              </Label>
              <p
                id={`${controlsId}-builder`}
                className={builderError ? s.error : s.muted}
              >
                {builderError && action.error instanceof Error
                  ? action.error.message
                  : builder === "railpack"
                    ? "Railpack 0.43.0 supports Node/npm, Go and shell projects here. Review the resource limits below; recommended limits are 6 GiB memory, 1 GiB source and 4 GiB builder storage."
                    : "Use a Dockerfile from your build context."}
              </p>
              {builder === "dockerfile" ? (
                <Label>
                  Dockerfile, relative to context
                  <Input
                    name="dockerfile"
                    value={dockerfile}
                    onChange={(e) => setDockerfile(e.target.value)}
                    required
                  />
                </Label>
              ) : (
                <>
                  <Label>
                    Build command override (optional)
                    <Textarea
                      name="buildCommand"
                      value={buildCommand}
                      onChange={(e) => setBuildCommand(e.target.value)}
                      maxLength={4096}
                      aria-invalid={builderError || undefined}
                      aria-describedby={`${controlsId}-overrides`}
                    />
                  </Label>
                  <Label>
                    Start command override (optional)
                    <Input
                      name="startCommand"
                      value={startCommand}
                      onChange={(e) => setStartCommand(e.target.value)}
                      maxLength={4096}
                      aria-invalid={builderError || undefined}
                      aria-describedby={`${controlsId}-overrides`}
                    />
                  </Label>
                  <p id={`${controlsId}-overrides`} className={s.muted}>
                    Leave empty to use detection and repository configuration.
                    These commands take precedence over repository commands.
                    Changing builders keeps your command drafts.
                  </p>
                </>
              )}
              <Label>
                Architecture
                <Select name="architecture">
                  <option value="arm64">Linux arm64</option>
                  <option value="amd64">Linux amd64</option>
                </Select>
              </Label>
              <details>
                <summary>Build resource limits</summary>
                <div className={s.stack}>
                  <p
                    id={`${controlsId}-budget`}
                    className={budgetError ? s.error : s.muted}
                  >
                    {budgetError && action.error instanceof Error
                      ? action.error.message
                      : "Limits apply to this assignment, including queue time. Source and builder storage share the memory limit; leave at least 96 MiB for tools. Custom limits require an updated agent."}
                  </p>
                  <Label>
                    CPU cores
                    <Input
                      type="number"
                      name="cpus"
                      min={1}
                      max={8}
                      step={1}
                      value={limits.cpus}
                      onChange={(e) =>
                        setLimits({ ...limits, cpus: e.target.value })
                      }
                      required
                    />
                  </Label>
                  <Label>
                    Memory limit (MiB)
                    <Input
                      type="number"
                      name="memoryMiB"
                      aria-invalid={budgetError || undefined}
                      aria-describedby={`${controlsId}-budget`}
                      min={512}
                      max={8192}
                      step={1}
                      value={limits.memoryMiB}
                      onChange={(e) =>
                        setLimits({ ...limits, memoryMiB: e.target.value })
                      }
                      required
                    />
                  </Label>
                  <Label>
                    Source storage (MiB)
                    <Input
                      type="number"
                      name="sourceMiB"
                      aria-invalid={budgetError || undefined}
                      aria-describedby={`${controlsId}-budget`}
                      min={64}
                      max={1024}
                      step={1}
                      value={limits.sourceMiB}
                      onChange={(e) =>
                        setLimits({ ...limits, sourceMiB: e.target.value })
                      }
                      required
                    />
                  </Label>
                  <Label>
                    Builder storage (MiB)
                    <Input
                      type="number"
                      name="stateMiB"
                      aria-invalid={budgetError || undefined}
                      aria-describedby={`${controlsId}-budget`}
                      min={128}
                      max={6144}
                      step={1}
                      value={limits.stateMiB}
                      onChange={(e) =>
                        setLimits({ ...limits, stateMiB: e.target.value })
                      }
                      required
                    />
                  </Label>
                  <Label>
                    Assignment lifetime (minutes)
                    <Input
                      type="number"
                      name="minutes"
                      min={1}
                      max={30}
                      step={1}
                      value={limits.minutes}
                      onChange={(e) =>
                        setLimits({ ...limits, minutes: e.target.value })
                      }
                      required
                    />
                  </Label>
                </div>
              </details>
              <Label>
                Registry destination
                <Select name="registry">
                  <option value="">Keep image on this runner</option>
                  {registries.data?.map((r) => (
                    <option key={r.id} value={r.id}>
                      {r.name} · {r.repository}
                    </option>
                  ))}
                </Select>
              </Label>
              <p className={s.muted}>
                A local image can deploy only on the runner that built it.
                Registry images can be promoted to another environment with
                matching architecture.
              </p>
              <Label>
                Plain build arguments
                <Textarea
                  name="arguments"
                  aria-invalid={inputsError || undefined}
                  aria-describedby={`${controlsId}-inputs`}
                  placeholder="PUBLIC_BASE_URL=https://example.com"
                />
              </Label>
              <Label>
                Build secret
                <Select
                  name="secret"
                  aria-invalid={inputsError || undefined}
                  aria-describedby={`${controlsId}-inputs`}
                >
                  <option value="">None</option>
                  {secrets.data?.map((v) => (
                    <option key={v.id} value={v.id}>
                      {v.name} · {new Date(v.createdAt).toLocaleString()}
                    </option>
                  ))}
                </Select>
              </Label>
              <Label>
                {builder === "railpack"
                  ? "Secret environment name"
                  : "Secret mount name"}
                <Input
                  name="secretName"
                  aria-invalid={inputsError || undefined}
                  aria-describedby={`${controlsId}-inputs`}
                />
              </Label>
              <p
                id={`${controlsId}-inputs`}
                className={inputsError ? s.error : s.muted}
              >
                {inputsError && action.error instanceof Error
                  ? action.error.message
                  : builder === "railpack"
                    ? "Declared build secrets are available as build environment variables, separately from runtime secrets. Use distinct public argument and secret names; RAILPACK_ settings must be public arguments. Build commands must avoid copying or printing secrets."
                    : "Build secrets are separate from runtime secrets and available through Dockerfile secret mounts. Your Dockerfile must avoid copying or printing them."}
              </p>
              <Label>
                <Checkbox name="autoBuild" />
                Build new branch commits from authenticated push webhooks
              </Label>
              <Label>
                Automatic build watch paths
                <Textarea
                  name="watchPaths"
                  placeholder={"apps/web\npnpm-lock.yaml"}
                  aria-invalid={watchError || undefined}
                  aria-describedby={`${controlsId}-watch`}
                />
              </Label>
              <p
                id={`${controlsId}-watch`}
                className={watchError ? s.error : s.muted}
              >
                {watchError && action.error instanceof Error
                  ? action.error.message
                  : "One distinct repository-relative file or directory per line, up to 16. Omit trailing slashes; directories include their contents. Wildcards are unsupported. Leave empty to build every change. Include shared configuration and lockfiles. Manual builds always run. Automatic builds compare with the last compatible successful build and rebuild when comparison is unavailable. Requires an updated agent."}
              </p>
              {owner && (
                <Label>
                  <Checkbox name="autoDeploy" />
                  Promote successful builds and automatically deploy after the
                  first reviewed deployment
                </Label>
              )}
              <Button type="submit" disabled={dirty || action.isPending}>
                Queue build
              </Button>
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
                const next = await result(
                  client.POST(`${root}/artifacts/{artifactId}/promote`, {
                    params: { path: { ...path, artifactId: artifact.id } },
                    body: {
                      applicationId: text(d, "app"),
                      baseRevisionId: revision.id,
                      inputHash: artifact.inputHash,
                    },
                  }),
                );
                await publishDraft(queries, path, next);
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
      <section className={s.panel}>
        <h3>Source and build jobs</h3>
        {!runs.data?.length && (
          <p className={s.muted}>No source or build jobs yet.</p>
        )}
        {runs.data?.map((run) => {
          const report = run.result;
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
              {report?.railpack && (
                <details>
                  <summary>
                    Railpack {report.railpack.version} ·{" "}
                    {report.railpack.provider}
                  </summary>
                  <p className={s.muted}>
                    Frozen planner inputs and resolved tools for this build.
                  </p>
                  <pre
                    className={s.code}
                    style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}
                  >{`Frontend: ${report.railpack.frontend}\nPlan: ${report.railpack.planHash}\n${Object.entries(
                    report.railpack.packages,
                  )
                    .sort(([a], [b]) => a.localeCompare(b))
                    .map(([name, version]) => `${name}: ${version}`)
                    .join("\n")}`}</pre>
                </details>
              )}
              {report?.reviewUrl && (
                <Anchor
                  href={report.reviewUrl}
                  target="_blank"
                  rel="noreferrer"
                >
                  Open source review
                </Anchor>
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
                  <Button
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
                  </Button>
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
                          const next = await result(
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
                          await publishDraft(queries, path, next);
                          setNotice(
                            "Artifact promoted to the saved draft. Use Reload current, then review a deployment plan. Runtime variables and bindings are preserved.",
                          );
                        })
                      }
                    >
                      <Label>
                        Destination service
                        <Select name="app">
                          {revision.specification.applications
                            .filter((a) => a.kind !== "postgres")
                            .map((a) => (
                              <option key={a.id} value={a.id}>
                                {a.name}
                              </option>
                            ))}
                        </Select>
                      </Label>
                      <Button
                        type="submit"
                        disabled={dirty || action.isPending}
                      >
                        Promote artifact to draft
                      </Button>
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
