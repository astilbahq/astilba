import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { client, result, type Schema } from "../api/client";
import { publishDraft } from "../api/environment";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Input } from "../components/input";
import { Label } from "../components/label";
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

export const wsRoot = "/api/v1/workspaces/{workspaceId}" as const;
export const root =
  `${wsRoot}/projects/{projectId}/environments/{environmentId}` as const;
const policyRoot = `${root}/backup-policies/{policyId}` as const;
export const nil = "00000000-0000-0000-0000-000000000000";
export function PolicyStatus({
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
  // Every action here hands the policy's credentials to its server or changes them, so each
  // needs a recent confirmation of identity.
  const action = useAction({ recentAuth: true }),
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
                        await publishDraft(queries, path, next, revision.id);
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
