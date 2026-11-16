import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import {
  backupDestinationsOptions,
  backupDestinationsPath,
  backupPoliciesPath,
  nilID,
} from "../api/backups";
import { ApiError, client, result, type Schema } from "../api/client";
import { serversOptions } from "../api/servers";
import { Checkbox } from "../components/checkbox";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "../ui";
import { PolicyStatus } from "./backup-policy-status";
import {
  BackupRecommendations,
  type RecommendedBackup,
} from "./backup-recommendations";
import { ExposureAcknowledgement } from "./backup-recovery";
import {
  ScheduleFields,
  scheduleInput,
  scheduleTimeout,
} from "./schedule-fields";
import type { EnvironmentPath } from "./visual-editor";

export function Backups({
  path,
  revision,
  owner,
  dirty,
  load,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  owner: boolean;
  dirty: boolean;
  load: (revision: Schema["Revision"]) => boolean;
}) {
  const action = useAction(),
    queries = useQueryClient();
  const [method, setMethod] =
    useState<Schema["BackupPolicyRequest"]["method"]>("postgres-logical");
  const destinations = useQuery(backupDestinationsOptions(path.workspaceId));
  const policies = useQuery({
    queryKey: ["backup-policies", path],
    queryFn: () => result(client.GET(backupPoliciesPath, { params: { path } })),
  });
  const servers = useQuery(serversOptions(path.workspaceId));
  const [destination, setDestination] = useState("");
  const exposed = destinations.data?.find((d) => d.id === destination)?.exposed;
  const [preset, setPreset] = useState<RecommendedBackup>();
  const createPolicy = useRef<HTMLDetailsElement>(null);
  return (
    <section className={s.stack} aria-label="Backups and scheduled jobs">
      <h2>Backups and scheduled jobs</h2>
      <p>
        Backups catch up once after a missed schedule. Container jobs skip
        missed runs. Schedules continue while management is offline and share
        the host execution lock with deployments; runs never overlap.
      </p>
      <ErrorNotice
        error={
          action.error ?? destinations.error ?? policies.error ?? servers.error
        }
      />
      {owner && (
        <>
          <details className={s.panel}>
            <summary>Add a reusable backup destination</summary>
            <p>
              Filesystem destinations must use a root enabled by the server
              operator and share the server’s failure boundary. S3 data travels
              directly from the agent to your storage.
            </p>
            <Form
              action={action}
              label="Add backup destination"
              onSubmit={(d, form) =>
                action.run(async () => {
                  await result(
                    client.POST(backupDestinationsPath, {
                      params: { path },
                      body: {
                        name: text(d, "name"),
                        kind: text(d, "kind") as "filesystem" | "s3",
                        root: text(d, "root"),
                        endpoint: text(d, "endpoint"),
                        bucket: text(d, "bucket"),
                        region: text(d, "region"),
                        access: {
                          accessKey: text(d, "key"),
                          secretKey: text(d, "secret"),
                          password: "",
                        },
                      },
                    }),
                  );
                  form.reset();
                  await queries.invalidateQueries({
                    queryKey: ["backup-destinations", path.workspaceId],
                  });
                })
              }
            >
              <Field name="name" label="Destination name" />
              <Label>
                Storage type
                <Select name="kind">
                  <option value="filesystem">Filesystem</option>
                  <option value="s3">S3 compatible</option>
                </Select>
              </Label>
              <Field
                name="root"
                label="Operator-approved filesystem root"
                required={false}
              />
              <Field name="endpoint" label="S3 HTTPS origin" required={false} />
              <Field name="bucket" label="S3 bucket" required={false} />
              <Field name="region" label="S3 region" required={false} />
              <Field
                name="key"
                label="S3 access key"
                type="password"
                autoComplete="off"
                required={false}
              />
              <Field
                name="secret"
                label="S3 secret key"
                type="password"
                autoComplete="off"
                required={false}
              />
              <BusyButton busy={action.isPending}>Add destination</BusyButton>
            </Form>
          </details>
          <BackupRecommendations
            path={path}
            revision={revision}
            policies={policies.data}
            form={createPolicy}
            choose={(r) => {
              setPreset(r);
              setMethod(r.method);
            }}
          />
          <details className={s.panel} ref={createPolicy}>
            <summary>Create backup policy or container job</summary>
            <p>
              Requires an applied deployment. Keep the repository password and
              storage credentials in your own recovery records; they are never
              returned by the dashboard. Refresh and install the policy after
              changing its deployment.
            </p>
            <Form
              action={action}
              label="Create backup policy"
              onSubmit={(d, form) =>
                action.run(async () => {
                  await result(
                    client.POST(backupPoliciesPath, {
                      params: { path },
                      body: {
                        name: text(d, "name"),
                        resourceId: text(d, "resource"),
                        destinationId:
                          method === "container-job"
                            ? nilID
                            : text(d, "destination"),
                        method,
                        schedule: scheduleInput(d),
                        timeoutSeconds: scheduleTimeout(d),
                        retain: Number(text(d, "retain")),
                        enabled: d.has("enabled"),
                        command:
                          method === "container-job"
                            ? JSON.parse(text(d, "command"))
                            : [],
                        password: text(d, "password"),
                        baseGeneration: 0,
                        exposureAcknowledged: d.has("exposureAcknowledged")
                          ? true
                          : undefined,
                      },
                    }),
                  ).catch(async (error: unknown) => {
                    // A destination exposed since the list loaded is refused until it is
                    // acknowledged, so the list is refreshed to offer the acknowledgement.
                    if (
                      error instanceof ApiError &&
                      error.detail.fields?.includes("destination.exposed")
                    )
                      await queries.invalidateQueries({
                        queryKey: backupDestinationsOptions(path.workspaceId)
                          .queryKey,
                      });
                    throw error;
                  });
                  form.reset();
                  setDestination("");
                  setPreset(undefined);
                  await queries.invalidateQueries({
                    queryKey: ["backup-policies", path],
                  });
                })
              }
            >
              <Field
                key={`name-${preset?.serviceId}`}
                name="name"
                label="Policy name"
                defaultValue={preset && `${preset.app.name} daily backup`}
              />
              <Label>
                Consistency method
                <Select
                  value={method}
                  onChange={(e) => setMethod(e.target.value as typeof method)}
                >
                  <option value="postgres-logical">
                    PostgreSQL logical backup
                  </option>
                  <option value="quiesced-files">Quiesced file volume</option>
                  <option value="container-job">Scheduled container job</option>
                </Select>
              </Label>
              <Label>
                Source resource
                <Select
                  key={preset?.serviceId}
                  name="resource"
                  required
                  defaultValue={preset?.serviceId ?? ""}
                >
                  <option value="">Choose a resource</option>
                  {method === "quiesced-files"
                    ? revision.specification.volumes?.map((v) => (
                        <option value={v.id} key={v.id}>
                          {v.name}
                        </option>
                      ))
                    : revision.specification.applications
                        .filter((a) =>
                          method === "postgres-logical"
                            ? !!a.postgres
                            : a.kind === "worker",
                        )
                        .map((a) => (
                          <option value={a.id} key={a.id}>
                            {a.name}
                          </option>
                        ))}
                </Select>
              </Label>
              {method !== "container-job" && (
                <>
                  <Label>
                    Backup destination
                    <Select
                      name="destination"
                      required
                      value={destination}
                      onChange={(e) => setDestination(e.target.value)}
                    >
                      <option value="">Choose a destination</option>
                      {destinations.data?.map((d) => (
                        <option key={d.id} value={d.id}>
                          {d.name}
                          {d.kind === "filesystem"
                            ? " — same server failure boundary"
                            : ""}
                          {d.exposed
                            ? " — keys exposed to a revoked server"
                            : ""}
                        </option>
                      ))}
                    </Select>
                  </Label>
                  {exposed && <ExposureAcknowledgement />}
                  <Field
                    name="password"
                    label="Repository password (keep a recovery copy)"
                    type="password"
                    minLength={16}
                    autoComplete="new-password"
                  />
                </>
              )}
              {method === "container-job" && (
                <Field
                  name="command"
                  label='Command as JSON arguments, such as ["node", "job.js"]'
                />
              )}
              <ScheduleFields
                key={preset?.serviceId}
                initial={
                  preset && {
                    hour: preset.hour,
                    minute: preset.minute,
                    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
                  }
                }
              />
              {method !== "container-job" ? (
                <Label>
                  Successful uploads to retain
                  <Input
                    key={preset?.serviceId}
                    name="retain"
                    type="number"
                    min="1"
                    max="100"
                    defaultValue={preset?.retain ?? 7}
                    required
                  />
                </Label>
              ) : (
                <Input name="retain" type="hidden" value="1" />
              )}
              <Label>
                <Checkbox name="enabled" defaultChecked />
                Enable schedule when installed
              </Label>
              <BusyButton busy={action.isPending}>Create policy</BusyButton>
            </Form>
          </details>
        </>
      )}
      {policies.data?.map((policy) => (
        <PolicyStatus
          key={policy.id}
          path={path}
          policy={policy}
          server={servers.data?.find((v) => v.id === policy.serverId)}
          exposed={
            !!destinations.data?.find((d) => d.id === policy.destinationId)
              ?.exposed
          }
          owner={owner}
          revision={revision}
          dirty={dirty}
          load={load}
        />
      ))}
    </section>
  );
}
