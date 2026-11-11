import { type ReactNode, useId, useState } from "react";
import { ApiError, client, result, type Schema } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { Button } from "../../components/button";
import { Checkbox } from "../../components/checkbox";
import { Input } from "../../components/input";
import { Label } from "../../components/label";
import { Select } from "../../components/select";
import { Textarea } from "../../components/textarea";
import * as s from "../../styles";
import { Form, text, type useAction } from "../../ui";
import { AutomaticDeploymentScope } from "./automatic-deployment-scope";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;
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
export function BuildServiceForm({
  path,
  revision,
  write,
  owner,
  dirty,
  action,
  connection,
  runner,
  repositoryFields,
  servers,
  registries,
  secrets,
  onQueued,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  write: boolean;
  owner: boolean;
  dirty: boolean;
  action: ReturnType<typeof useAction>;
  connection: string;
  runner: string;
  repositoryFields: ReactNode;
  servers: Schema["Server"][] | undefined;
  registries: Schema["Registry"][] | undefined;
  secrets: Schema["BuildSecret"][] | undefined;
  onQueued: () => Promise<void>;
}) {
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
  if (!write) return null;
  return (
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
              for (const id of [runner, text(d, "fallback")].filter(Boolean)) {
                if (
                  !servers?.find((server) => server.id === id)?.capabilities
                    ?.buildControls
                ) {
                  throw new Error(
                    "Update the selected runner and fallback agents before using custom build limits or watch paths.",
                  );
                }
              }
            }
            if (builder === "railpack") {
              for (const id of [runner, text(d, "fallback")].filter(Boolean)) {
                if (
                  servers?.find((server) => server.id === id)?.capabilities
                    ?.railpackVersion !== "0.43.0"
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
                    builder: builder === "railpack" ? "railpack" : undefined,
                    dockerfile:
                      builder === "dockerfile" ? dockerfile : undefined,
                    railpack:
                      builder === "railpack" && (buildCommand || startCommand)
                        ? { buildCommand, startCommand }
                        : undefined,
                    architecture: text(d, "architecture") as "amd64" | "arm64",
                    arguments: argumentsMap,
                    secrets: secretMap,
                    budget: customBudget ? budget : undefined,
                    watchPaths: watchPaths.length ? watchPaths : undefined,
                    autoBuild: d.has("autoBuild"),
                    autoDeploy: d.has("autoDeploy"),
                  },
                },
              }),
            );
            await onQueued();
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
              Leave empty to use detection and repository configuration. These
              commands take precedence over repository commands. Changing
              builders keeps your command drafts.
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
                onChange={(e) => setLimits({ ...limits, cpus: e.target.value })}
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
            {registries?.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name} · {r.repository}
              </option>
            ))}
          </Select>
        </Label>
        <p className={s.muted}>
          A local image can deploy only on the runner that built it. Registry
          images can be promoted to another environment with matching
          architecture.
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
            {secrets?.map((v) => (
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
          <>
            <Label>
              <Checkbox name="autoDeploy" />
              Promote successful builds and automatically deploy after the first
              reviewed deployment
            </Label>
            <AutomaticDeploymentScope />
          </>
        )}
        <Button type="submit" disabled={dirty || action.isPending}>
          Queue build
        </Button>
      </Form>
    </details>
  );
}
