import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { client, result, type Schema } from "../api/client";
import { draftOptions } from "../api/environment";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Form, text, useAction } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/upgrades" as const;
const policyRoot =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/backup-policies" as const;
type Action = Schema["UpgradeActionRequest"]["action"];

export function Upgrades({
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
  const queries = useQueryClient(),
    action = useAction();
  const keys = useRef(new Map<string, string>());
  const [comparison, setComparison] = useState<{
    id: string;
    revision: Schema["Revision"];
  }>();
  const upgrades = useQuery({
    queryKey: ["upgrades", path],
    queryFn: () => result(client.GET(root, { params: { path } })),
    refetchInterval: 5000,
  });
  const policies = useQuery({
    queryKey: ["backup-policies", path],
    queryFn: () => result(client.GET(policyRoot, { params: { path } })),
  });
  const run = (v: Schema["UpgradeView"], name: Action) =>
    action.run(async () => {
      const identity = `${v.plan.id}:${name}`;
      const key = keys.current.get(identity) ?? crypto.randomUUID();
      keys.current.set(identity, key);
      await result(
        client.POST(`${root}/{upgradeId}/actions`, {
          params: { path: { ...path, upgradeId: v.plan.id } },
          body: {
            action: name,
            requestKey: key,
            expectedPhase: v.status.phase,
          },
        }),
      );
      keys.current.delete(identity);
      await queries.invalidateQueries({ queryKey: ["upgrades", path] });
      await queries.invalidateQueries({
        queryKey: ["operations", path.workspaceId],
      });
    });
  return (
    <section className={s.stack} aria-label="Supported major upgrades">
      <h2>Supported major upgrades</h2>
      <p>
        PostgreSQL 17.10 → 18.4 uses a logical backup and fresh storage. All
        services in this environment stop until you approve cutover or return to
        the old database. Duration depends on data size, storage and network
        speed.
      </p>
      <ErrorNotice error={action.error ?? upgrades.error ?? policies.error} />
      {owner && (
        <details className={s.panel}>
          <summary>Review a PostgreSQL 17 → 18 upgrade</summary>
          <p>
            Requires one managed PostgreSQL 17.10 database, no external clients,
            local Docker storage, standard tablespaces and only plpgsql or
            hstore extensions. Additional login roles, role memberships,
            replication slots and subscriptions are unsupported. The server
            checks these conditions before stopping services.
          </p>
          <p>
            Reserve free host space for three times the database size, plus 256
            MiB, and staging space for twice its size, plus 256 MiB. The agent
            also checks the actual archive size before restore. Keep the backup
            destination credentials in your recovery records.
          </p>
          <Form
            action={action}
            label="Review major upgrade"
            onSubmit={(d) =>
              action.run(async () => {
                const policy = policies.data?.find(
                  (p) => p.id === text(d, "policy"),
                );
                if (!policy)
                  throw new Error("Select a PostgreSQL backup policy.");
                await result(
                  client.POST(root, {
                    params: { path },
                    body: {
                      policyId: policy.id,
                      resourceId: policy.resourceId,
                      baseRevisionId: revision.id,
                      retainDays: Number(text(d, "retainDays")),
                    },
                  }),
                );
                await queries.invalidateQueries({
                  queryKey: ["upgrades", path],
                });
              })
            }
          >
            <Label>
              Final backup policy
              <Select name="policy" required>
                <option value="">Select a policy</option>
                {policies.data
                  ?.filter((p) => p.method === "postgres-logical")
                  .map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
              </Select>
            </Label>
            <Label>
              Minimum days to retain old storage and the recovery snapshot
              <Input
                name="retainDays"
                type="number"
                min={1}
                max={365}
                defaultValue="30"
                required
              />
            </Label>
            <p>
              Retention prevents platform cleanup. Nothing is automatically
              deleted when it expires; deletion is a separate action.
            </p>
            <Button
              type="submit"
              variant="primary"
              disabled={dirty || action.isPending}
            >
              Create upgrade plan
            </Button>
          </Form>
        </details>
      )}
      {upgrades.data?.map((v) => (
        <article key={v.plan.id} className={s.panel}>
          <h3>PostgreSQL 17 → 18 · {v.status.phase}</h3>
          <p role="status">
            {v.status.phase}: {v.status.note}
          </p>
          <dl>
            <dt>Last step</dt>
            <dd>{v.status.step || "Awaiting review"}</dd>
            <dt>Serving database</dt>
            <dd>{v.status.serving}</dd>
            <dt>Old storage protected until</dt>
            <dd>
              {v.status.phase === "planned"
                ? `${v.plan.retainDays} days after completion`
                : new Date(v.status.keepUntil).toLocaleString()}
            </dd>
            <dt>Recovery snapshot</dt>
            <dd>{v.status.snapshot || "Not yet created"}</dd>
          </dl>
          <p>
            {v.status.writesPossible
              ? "New writes may exist on PostgreSQL 18. Restarting the old volume would discard them. Recovery can only reconcile the new database."
              : "Before new writes become possible, you can explicitly return to the old database."}
          </p>
          <details>
            <summary>Ordered steps and storage identities</summary>
            <ol>
              <li>
                Check versions, extensions, roles, clients, storage and backup
                access.
              </li>
              <li>Stop all managed services and isolate the old database.</li>
              <li>Upload the final encrypted backup.</li>
              <li>
                Restore that uploaded backup into PostgreSQL 18 on fresh storage
                and verify schema and data.
              </li>
              <li>Pause for a separate cutover approval.</li>
              <li>
                Record the new-write boundary, activate the database, verify
                routes, then resume services.
              </li>
            </ol>
            <p>
              Source plan: {v.plan.source.id}. Target volume:{" "}
              {v.plan.targetVolumeId}. Recipe: {v.plan.recipe}.
            </p>
            <p>
              Existing backup schedules are disabled after cutover. Refresh
              their policies for the new deployment before reinstalling them.
            </p>
          </details>
          {comparison?.id === v.plan.id && (
            <section aria-label="Upgraded revision comparison">
              <p role="status">
                Your saved draft contains newer edits and was preserved. Compare
                it with the applied PostgreSQL 18 revision below before
                preparing another deployment.
              </p>
              <pre className={s.code}>
                {JSON.stringify(comparison.revision.specification, null, 2)}
              </pre>
            </section>
          )}
          {owner && (
            <div className={s.row}>
              {v.status.phase === "planned" && (
                <Form
                  action={action}
                  label="Start reviewed upgrade"
                  onSubmit={() => run(v, "prepare")}
                >
                  <Label>
                    <Checkbox required /> I accept downtime for every service in
                    this environment and have disconnected external database
                    clients.
                  </Label>
                  <Button
                    type="submit"
                    variant="primary"
                    disabled={dirty || action.isPending}
                  >
                    Stop services, back up and verify upgrade
                  </Button>
                </Form>
              )}
              {v.status.phase === "ready" && (
                <Form
                  action={action}
                  label="Approve database cutover"
                  onSubmit={() => run(v, "cutover")}
                >
                  <Label>
                    <Checkbox required /> Allow new writes on PostgreSQL 18.
                    Returning to the old volume afterward can lose data.
                  </Label>
                  <BusyButton busy={action.isPending}>
                    Approve cutover and resume services
                  </BusyButton>
                </Form>
              )}
              {(v.status.phase === "ready" || v.status.phase === "attention") &&
                !v.status.writesPossible && (
                  <Button
                    type="button"
                    disabled={action.isPending}
                    onClick={() => run(v, "return")}
                  >
                    Return to old database
                  </Button>
                )}
              {v.status.phase === "attention" && v.status.writesPossible && (
                <Button
                  type="button"
                  disabled={action.isPending}
                  onClick={() => run(v, "reconcile")}
                >
                  Reconcile new database and resume services
                </Button>
              )}
              {v.status.phase === "complete" && (
                <Button
                  type="button"
                  disabled={action.isPending}
                  onClick={() =>
                    action.run(async () => {
                      const [current, revision] = await Promise.all([
                        queries.fetchQuery({
                          ...draftOptions(path),
                          staleTime: 0,
                        }),
                        result(
                          client.GET(
                            "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/revisions/{revisionId}",
                            {
                              params: {
                                path: {
                                  ...path,
                                  revisionId: v.plan.targetRevisionId,
                                },
                              },
                            },
                          ),
                        ),
                      ]);
                      if (current.id !== revision.id) {
                        setComparison({ id: v.plan.id, revision });
                        return;
                      }
                      if (!load(revision))
                        throw new Error(
                          "Your newer edits were retained. Compare them with the upgraded revision.",
                        );
                      setComparison(undefined);
                    })
                  }
                >
                  Load upgraded configuration
                </Button>
              )}
            </div>
          )}
        </article>
      ))}
    </section>
  );
}
