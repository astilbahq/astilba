import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import { gitConnectionsOptions } from "../api/integrations";
import { serversOptions } from "../api/servers";
import { Checkbox } from "../components/checkbox";
import { Label } from "../components/label";
import { Select } from "../components/select";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "../ui";
import * as previewStyles from "./previews.styles";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;

// A preview template's automatic preview policy, which only an owner of an active template edits.
export function PreviewPolicyEditor({
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
  const connections = useQuery(gitConnectionsOptions(path.workspaceId));
  const servers = useQuery({
    ...serversOptions(path.workspaceId),
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
        You’ll be asked to confirm your identity before enabling, approving or
        removing previews.
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
