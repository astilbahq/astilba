import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import {
  backupDestinationsOptions,
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
import { BackupDestinationForm } from "./backup-destination-form";
import { ExposureAcknowledgement } from "./backup-exposure";
import { PolicyStatus } from "./backup-policy-status";
import {
  BackupRecommendations,
  type RecommendedBackup,
} from "./backup-recommendations";
import {
  ScheduleFields,
  scheduleInput,
  scheduleTimeout,
} from "./schedule-fields";
import type { EnvironmentPath } from "./visual-editor";

// A managed destination whose key has never worked, as management reports it: its key is still
// being made, or making it failed. A policy can't use it until a key has been active.
const placeholder = (d: Schema["BackupDestination"]) => d.placeholder;

export function Backups({
  path,
  revision,
  server,
  owner,
  dirty,
  load,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  /** The environment's deployment server, which its new policies are on. */
  server: string | null | undefined;
  owner: boolean;
  dirty: boolean;
  load: (revision: Schema["Revision"]) => boolean;
}) {
  // Destinations and policies take storage credentials, which the server accepts only from a
  // recent confirmation of identity.
  const action = useAction({ recentAuth: true }),
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
  const chosen = destinations.data?.find((d) => d.id === destination);
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
          <BackupDestinationForm
            path={path}
            servers={servers.data}
            action={action}
          />
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
              onSubmit={(d, form) => {
                let command: string[] = [];
                if (method === "container-job")
                  try {
                    command = JSON.parse(text(d, "command"));
                  } catch {
                    return action.reportInvalid(
                      form.elements.namedItem("command"),
                      'Enter the command as JSON arguments, such as ["node", "job.js"].',
                    );
                  }
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
                        command,
                        password: text(d, "password"),
                        baseGeneration: 0,
                        exposureAcknowledged: d.has("exposureAcknowledged")
                          ? true
                          : undefined,
                      },
                    }),
                  ).catch(async (error: unknown) => {
                    // A destination exposed, bound to a server, or whose key isn't ready since
                    // the list loaded is refused, so the list is refreshed to show why.
                    if (
                      error instanceof ApiError &&
                      error.detail.fields?.some((f) =>
                        [
                          "destination.exposed",
                          "destination.server",
                          "destination.shared",
                          "destination.key",
                        ].includes(f),
                      )
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
                  // The policy may have bound its destination to this server.
                  await Promise.all([
                    queries.invalidateQueries({
                      queryKey: ["backup-policies", path],
                    }),
                    queries.invalidateQueries({
                      queryKey: backupDestinationsOptions(path.workspaceId)
                        .queryKey,
                    }),
                  ]);
                });
              }}
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
                        // An S3 destination's keys belong to one server, and only its policies
                        // can use it. A managed one needs its key made first.
                        <option
                          key={d.id}
                          value={d.id}
                          disabled={
                            d.shared ||
                            (!!d.serverId && d.serverId !== server) ||
                            placeholder(d)
                          }
                        >
                          {d.name}
                          {d.kind === "filesystem"
                            ? " — same server failure boundary"
                            : d.shared
                              ? " — shared by several servers, closed to new policies"
                              : d.serverId && d.serverId !== server
                                ? ` — keys belong to ${servers.data?.find((v) => v.id === d.serverId)?.name ?? "another server"}`
                                : placeholder(d)
                                  ? " — its key is still being made, or making it failed"
                                  : ""}
                          {d.accountId ? " — keys made by management" : ""}
                          {d.exposed
                            ? " — keys exposed to a revoked server"
                            : ""}
                        </option>
                      ))}
                    </Select>
                  </Label>
                  {chosen?.exposed && (
                    <ExposureAcknowledgement managed={!!chosen.accountId} />
                  )}
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
      {policies.data?.map((policy) => {
        // A container job is never given a destination's keys.
        const used =
          policy.method !== "container-job"
            ? destinations.data?.find((d) => d.id === policy.destinationId)
            : undefined;
        return (
          <PolicyStatus
            key={policy.id}
            path={path}
            policy={policy}
            server={servers.data?.find((v) => v.id === policy.serverId)}
            exposed={
              used?.exposed ? (used.accountId ? "managed" : "owner") : undefined
            }
            owner={owner}
            revision={revision}
            dirty={dirty}
            load={load}
          />
        );
      })}
    </section>
  );
}
