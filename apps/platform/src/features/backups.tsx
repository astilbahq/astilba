import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { client, result, type Schema } from "../api/client";
import { publishDraft } from "../api/environment";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "../ui";
import {
  ScheduleFields,
  scheduleInput,
  scheduleTimeout,
  scheduleDescription,
  nextRunDescription,
} from "./schedule-fields";
import type { EnvironmentPath } from "./visual-editor";

const wsRoot = "/api/v1/workspaces/{workspaceId}" as const;
const root =
  `${wsRoot}/projects/{projectId}/environments/{environmentId}` as const;
const policyRoot = `${root}/backup-policies/{policyId}` as const;
const nil = "00000000-0000-0000-0000-000000000000";
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
  const destinations = useQuery({
    queryKey: ["backup-destinations", path.workspaceId],
    queryFn: () =>
      result(client.GET(`${wsRoot}/backup-destinations`, { params: { path } })),
  });
  const policies = useQuery({
    queryKey: ["backup-policies", path],
    queryFn: () =>
      result(client.GET(`${root}/backup-policies`, { params: { path } })),
  });
  return (
    <section className={s.stack} aria-label="Backups and scheduled jobs">
      <h2>Backups and scheduled jobs</h2>
      <p>
        Backups catch up once after a missed schedule. Container jobs skip
        missed runs. Schedules continue while management is offline and share
        the host execution lock with deployments; runs never overlap.
      </p>
      <ErrorNotice
        error={action.error ?? destinations.error ?? policies.error}
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
                    client.POST(`${wsRoot}/backup-destinations`, {
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
          <details className={s.panel}>
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
                    client.POST(`${root}/backup-policies`, {
                      params: { path },
                      body: {
                        name: text(d, "name"),
                        resourceId: text(d, "resource"),
                        destinationId:
                          method === "container-job"
                            ? nil
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
                      },
                    }),
                  );
                  form.reset();
                  await queries.invalidateQueries({
                    queryKey: ["backup-policies", path],
                  });
                })
              }
            >
              <Field name="name" label="Policy name" />
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
                <Select name="resource" required>
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
                    <Select name="destination" required>
                      <option value="">Choose a destination</option>
                      {destinations.data?.map((d) => (
                        <option key={d.id} value={d.id}>
                          {d.name}
                          {d.kind === "filesystem"
                            ? " — same server failure boundary"
                            : ""}
                        </option>
                      ))}
                    </Select>
                  </Label>
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
              <ScheduleFields />
              {method !== "container-job" ? (
                <Label>
                  Successful uploads to retain
                  <Input
                    name="retain"
                    type="number"
                    min="1"
                    max="100"
                    defaultValue="7"
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
          owner={owner}
          revision={revision}
          dirty={dirty}
          load={load}
        />
      ))}
    </section>
  );
}
function PolicyStatus({
  path,
  policy,
  owner,
  revision,
  dirty,
  load,
}: {
  path: EnvironmentPath;
  policy: Schema["BackupPolicy"];
  owner: boolean;
  revision: Schema["Revision"];
  dirty: boolean;
  load: (revision: Schema["Revision"]) => boolean;
}) {
  const action = useAction(),
    queries = useQueryClient(),
    p = { ...path, policyId: policy.id };
  const [message, setMessage] = useState("");
  const [editing, setEditing] = useState<Schema["BackupPolicy"]>();
  const status = useQuery({
    queryKey: ["backup-status", p],
    queryFn: () =>
      result(client.GET(`${policyRoot}/status`, { params: { path: p } })),
    refetchInterval: 5000,
  });
  const attempts = useRef(new Map<string, string>());
  const command = (
    verb: Schema["BackupCommandRequest"]["action"],
    snapshot = "",
  ) =>
    action.run(async () => {
      const attempt = `${policy.generation}:${verb}:${snapshot}`;
      const requestKey = attempts.current.get(attempt) ?? crypto.randomUUID();
      attempts.current.set(attempt, requestKey);
      await result(
        client.POST(`${policyRoot}/commands`, {
          params: { path: p },
          body: { action: verb, requestKey, snapshot },
        }),
      );
      attempts.current.delete(attempt);
      setMessage("Action queued. Check operation history and the run result.");
      await queries.invalidateQueries({ queryKey: ["backup-status", p] });
    });
  const runs = status.data?.runs ?? [],
    last = runs.findLast(
      (r) => r.state === "succeeded" && !!r.snapshot && !r.pruned,
    );
  return (
    <section className={s.panel} aria-label={`Policy ${policy.name}`}>
      <h3>{policy.name}</h3>
      <p>
        {policy.method}; {scheduleDescription(policy.schedule)}; timeout{" "}
        {policy.timeoutSeconds ?? 1800} seconds.{" "}
        {policy.method !== "container-job" &&
          `Retain ${policy.retain} successful uploads plus protected recovery points. `}
        {policy.method === "container-job"
          ? "Missed runs skipped; no overlap."
          : "Missed runs coalesced; no overlap."}
      </p>
      <p>
        {status.data?.generation === policy.generation
          ? `Installed generation ${status.data.generation}. ${status.data.enabled ? `Next run: ${nextRunDescription(status.data.next, policy.schedule)}` : "Local schedule disabled. After a rollback or local recovery, save and install a new generation to enable the configured schedule."}`
          : "Current policy is not installed."}
      </p>
      <p>
        Last successful snapshot:{" "}
        {last
          ? `${last.snapshot} (${new Date(last.startedAt).toLocaleString()})`
          : "None"}
      </p>
      <ErrorNotice error={action.error ?? status.error} />
      {message && <p role="status">{message}</p>}
      {owner && (
        <div className={s.row}>
          <Button
            type="button"
            disabled={action.isPending}
            onClick={() => command("install")}
          >
            Install schedule
          </Button>
          <Button
            type="button"
            disabled={action.isPending}
            onClick={() => command("check")}
            hidden={policy.method === "container-job"}
          >
            Check destination
          </Button>
          <Button
            type="button"
            disabled={action.isPending}
            onClick={() => command("backup")}
          >
            Run now
          </Button>
          <Button
            type="button"
            disabled={action.isPending}
            onClick={() => command("disable")}
          >
            Disable local schedule
          </Button>
        </div>
      )}
      {owner && (
        <details
          onToggle={(event) =>
            setEditing(event.currentTarget.open ? policy : undefined)
          }
        >
          <summary>Update schedule or refresh deployment</summary>
          <p>
            A new generation takes effect when you install it. Storage
            destination, source resource and consistency method stay fixed;
            create another policy to change them.
          </p>
          {editing && (
            <>
              <p>
                Editing generation {editing.generation}.
                {editing.generation !== policy.generation &&
                  " A newer generation is available. Your draft is kept; close and reopen this form to load the latest policy."}
              </p>
              <Form
                action={action}
                label={`Update ${editing.name}`}
                onSubmit={(d, form) =>
                  action.run(async () => {
                    const saved = await result(
                      client.PUT(policyRoot, {
                        params: { path: p },
                        body: {
                          name: editing.name,
                          resourceId: editing.resourceId,
                          destinationId: editing.destinationId,
                          method: editing.method,
                          schedule: scheduleInput(d),
                          timeoutSeconds: scheduleTimeout(d),
                          retain: Number(text(d, "retain")),
                          enabled: d.has("enabled"),
                          command: editing.command ?? [],
                          password: text(d, "password"),
                          baseGeneration: editing.generation,
                        },
                      }),
                    );
                    setEditing(saved);
                    form.reset();
                    await queries.invalidateQueries({
                      queryKey: ["backup-policies", path],
                    });
                    setMessage(
                      "Policy updated. Install the new generation to activate it.",
                    );
                  })
                }
              >
                <ScheduleFields
                  key={editing.generation}
                  initial={editing.schedule}
                  timeout={editing.timeoutSeconds ?? 1800}
                />
                {editing.method !== "container-job" ? (
                  <Label>
                    Successful uploads to retain
                    <Input
                      type="number"
                      name="retain"
                      min="1"
                      max="100"
                      defaultValue={editing.retain}
                      required
                    />
                  </Label>
                ) : (
                  <Input name="retain" type="hidden" value="1" />
                )}
                <Label>
                  <Checkbox name="enabled" defaultChecked={editing.enabled} />
                  Enable schedule when installed
                </Label>
                {editing.method !== "container-job" && (
                  <Field
                    name="password"
                    label="Existing repository password"
                    type="password"
                    autoComplete="off"
                    minLength={16}
                  />
                )}
                <BusyButton busy={action.isPending}>
                  Save policy generation
                </BusyButton>
              </Form>
            </>
          )}
        </details>
      )}
      <ol>
        {runs.map((r) => (
          <li key={r.id}>
            <p>
              {new Date(r.scheduledAt).toLocaleString()}: {r.state}
              {r.exitCode !== null ? `; exit ${r.exitCode}` : ""}. {r.note}
            </p>
            {r.snapshot && (
              <p>
                Snapshot {r.snapshot}:{" "}
                {r.pruned
                  ? "pruned"
                  : r.verified
                    ? "restore verified"
                    : "uploaded, restore unverified"}
                {r.protected ? "; protected from retention" : ""}
              </p>
            )}
            {owner &&
              r.state === "succeeded" &&
              r.snapshot &&
              !r.pruned &&
              r.targetId !== nil && (
                <p>Restored volume {r.targetId} remains separate.</p>
              )}
            {owner && r.state === "succeeded" && r.snapshot && !r.pruned && (
              <Button
                type="button"
                disabled={action.isPending}
                onClick={() => command("restore", r.snapshot)}
              >
                Restore snapshot into fresh storage
              </Button>
            )}
            {owner &&
              r.verified &&
              r.targetId !== nil &&
              r.state === "succeeded" && (
                <>
                  <p>
                    Cutover will use this older recovery point. Review downtime
                    and any writes made since the backup before applying the
                    deployment.
                  </p>
                  <Button
                    type="button"
                    disabled={action.isPending || dirty}
                    onClick={() =>
                      action.run(async () => {
                        const next = await result(
                          client.POST(`${policyRoot}/cutover`, {
                            params: { path: p },
                            body: { runId: r.id, baseRevisionId: revision.id },
                          }),
                        );
                        await publishDraft(queries, path, next);
                        const loaded = load(next);
                        setMessage(
                          loaded
                            ? "Restore cutover saved as a draft. Prepare and review a deployment plan to switch storage."
                            : "Restore cutover saved. Your newer edits were retained; resolve the revision conflict before deployment.",
                        );
                      })
                    }
                  >
                    Prepare cutover draft
                  </Button>
                </>
              )}
          </li>
        ))}
      </ol>
    </section>
  );
}
