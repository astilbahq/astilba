import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import { sessionOptions } from "../router";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "../ui";

const titles: Record<Schema["Alert"]["kind"], string> = {
  server_offline: "Server disconnected",
  disk_docker: "Docker filesystem pressure",
  disk_agent: "Agent filesystem pressure",
  service_runtime: "Managed service unavailable",
  service_health: "Managed service health check failed",
  backup_schedule: "Scheduled backup failed",
  deployment_problem: "Deployment needs review",
  build_problem: "Build needs review",
  backup_problem: "Backup operation needs review",
};
export function Alerts() {
  const session = useQuery(sessionOptions).data;
  const [selected, setSelected] = useState("");
  const workspace =
    session?.workspaces.find((w) => w.id === selected) ??
    session?.workspaces[0];
  return (
    <div className={s.stack}>
      <header>
        <h1>Alerts</h1>
        <p>
          Review observed problems and their recovery. Alerts never change your
          workloads.
        </p>
      </header>
      <label>
        Workspace
        <select
          value={workspace?.id ?? ""}
          onChange={(e) => setSelected(e.target.value)}
        >
          {session?.workspaces.map((w) => (
            <option key={w.id} value={w.id}>
              {w.name}
            </option>
          ))}
        </select>
      </label>
      {workspace && <Inbox key={workspace.id} workspace={workspace} />}
    </div>
  );
}
function Inbox({ workspace }: { workspace: Schema["Workspace"] }) {
  const path = { workspaceId: workspace.id };
  const queries = useQueryClient();
  const action = useAction();
  const [view, setView] = useState<"all" | "attention">("attention");
  const [before, setBefore] = useState<string>();
  const [pages, setPages] = useState<string[]>([]);
  const inbox = useQuery({
    queryKey: ["alerts", workspace.id, view, before],
    refetchInterval: 10000,
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/alerts", {
          params: { path, query: { view, before } },
        }),
      ),
  });
  return (
    <>
      <section className={s.panel} aria-label="Operational inbox">
        <div className={s.spread}>
          <h2>Operational inbox</h2>
          <label>
            Show
            <select
              value={view}
              onChange={(e) => {
                setView(e.target.value as "all" | "attention");
                setBefore(undefined);
                setPages([]);
              }}
            >
              <option value="attention">Needs attention</option>
              <option value="all">All history</option>
            </select>
          </label>
        </div>
        <p>
          Missing observations do not mean recovery. Resolved and event history
          is retained for 30 days, up to 1,000 entries per workspace.
        </p>
        <ErrorNotice error={inbox.error ?? action.error} />
        {inbox.isPending ? (
          <p role="status">Loading alerts…</p>
        ) : !inbox.data?.alerts.length ? (
          <p>No alerts in this view.</p>
        ) : (
          <div className={s.stack}>
            {inbox.data.alerts.map((alert) => (
              <article
                className={s.panel}
                key={alert.id}
                aria-label={`${titles[alert.kind]} ${alert.resourceId}`}
              >
                <div className={s.spread}>
                  <h3>{titles[alert.kind]}</h3>
                  <strong>
                    {alert.state === "event"
                      ? "Problem recorded"
                      : alert.state === "firing"
                        ? "Active"
                        : alert.reason === "monitoring_stopped"
                          ? "Monitoring ended"
                          : "Recovered"}
                  </strong>
                </div>
                <p>
                  Resource: <code>{alert.resourceId}</code>
                </p>
                <p>
                  Observed{" "}
                  <time dateTime={alert.openedAt}>
                    {new Date(alert.openedAt).toLocaleString()}
                  </time>
                  {alert.resolvedAt && (
                    <>
                      {" "}
                      · Ended{" "}
                      <time dateTime={alert.resolvedAt}>
                        {new Date(alert.resolvedAt).toLocaleString()}
                      </time>
                    </>
                  )}
                </p>
                {alert.acknowledgedAt ? (
                  <p>
                    Acknowledged{" "}
                    <time dateTime={alert.acknowledgedAt}>
                      {new Date(alert.acknowledgedAt).toLocaleString()}
                    </time>
                  </p>
                ) : (
                  workspace.role !== "viewer" && (
                    <button
                      type="button"
                      disabled={action.isPending}
                      onClick={() =>
                        action.run(async () => {
                          await result(
                            client.POST(
                              "/api/v1/workspaces/{workspaceId}/alerts/{alertId}/acknowledge",
                              {
                                params: {
                                  path: { ...path, alertId: alert.id },
                                },
                              },
                            ),
                          );
                          await queries.invalidateQueries({
                            queryKey: ["alerts", workspace.id],
                          });
                        })
                      }
                    >
                      Acknowledge alert
                    </button>
                  )
                )}
                {!!alert.deliveries.length && (
                  <details>
                    <summary>Notification delivery</summary>
                    <ul>
                      {Object.entries(
                        alert.deliveries.reduce<Record<string, number>>(
                          (groups, delivery) => {
                            const label = `${delivery.channel} · ${delivery.phase} · ${delivery.state} · ${delivery.attempts} attempts`;
                            groups[label] = (groups[label] ?? 0) + 1;
                            return groups;
                          },
                          {},
                        ),
                      ).map(([label, count]) => (
                        <li key={label}>
                          {label}
                          {count > 1 ? ` (${count} recipients)` : ""}
                        </li>
                      ))}
                    </ul>
                  </details>
                )}
              </article>
            ))}
          </div>
        )}
        <nav aria-label="Alert pages" className={s.spread}>
          {!!pages.length && (
            <button
              type="button"
              onClick={() => {
                setBefore(pages.at(-1) || undefined);
                setPages(pages.slice(0, -1));
              }}
            >
              Newer alerts
            </button>
          )}
          {inbox.data?.next && (
            <button
              type="button"
              onClick={() => {
                setPages([...pages, before ?? ""]);
                setBefore(inbox.data?.next);
              }}
            >
              Older alerts
            </button>
          )}
        </nav>
      </section>
      {workspace.role === "owner" && (
        <DeliverySettings workspaceId={workspace.id} />
      )}
    </>
  );
}
function DeliverySettings({ workspaceId }: { workspaceId: string }) {
  const [saved, setSaved] = useState(false);
  const settings = useQuery({
    queryKey: ["alert-settings", workspaceId],
    refetchOnWindowFocus: false,
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/alert-settings", {
          params: { path: { workspaceId } },
        }),
      ),
  });
  return (
    <section className={s.panel}>
      <h2>Notification delivery</h2>
      <p>
        Notifications are optional. Email goes to current verified workspace
        owners. Changing delivery settings requires a recent identity
        confirmation.
      </p>
      <ErrorNotice error={settings.error} />
      {settings.data ? (
        <SettingsForm
          key={`${settings.data.emailEnabled}-${settings.data.webhookEnabled}`}
          workspaceId={workspaceId}
          settings={settings.data}
          onSaved={setSaved}
        />
      ) : (
        settings.isPending && <p role="status">Loading delivery settings…</p>
      )}
      {saved && <p role="status">Notification settings saved.</p>}
    </section>
  );
}
function SettingsForm({
  workspaceId,
  settings,
  onSaved,
}: {
  workspaceId: string;
  settings: Schema["AlertSettings"];
  onSaved: (saved: boolean) => void;
}) {
  const action = useAction();
  const queries = useQueryClient();
  const [hook, setHook] = useState(settings.webhookEnabled);
  return (
    <Form
      action={action}
      label="Alert delivery settings"
      onSubmit={(d, form) =>
        action.run(async () => {
          onSaved(false);
          const url = text(d, "webhookUrl"),
            secret = String(d.get("webhookSecret") ?? "");
          const updated = await result(
            client.PUT("/api/v1/workspaces/{workspaceId}/alert-settings", {
              params: { path: { workspaceId } },
              body: {
                emailEnabled: d.has("emailEnabled"),
                webhookEnabled: hook,
                ...(url ? { webhookUrl: url } : {}),
                ...(secret ? { webhookSecret: secret } : {}),
              },
            }),
          );
          queries.setQueryData(["alert-settings", workspaceId], updated);
          form.reset();
          onSaved(true);
        })
      }
    >
      <ErrorNotice error={action.error} />
      <label>
        <input
          type="checkbox"
          name="emailEnabled"
          defaultChecked={settings.emailEnabled}
        />
        Email verified owners
      </label>
      <label>
        <input
          type="checkbox"
          checked={hook}
          onChange={(e) => setHook(e.target.checked)}
        />
        Send signed webhooks
      </label>
      {hook && (
        <>
          <p>
            {settings.webhookEnabled
              ? "A destination is configured. Leave both fields empty to keep it, or replace both."
              : "Enter a destination and signing secret. The secret will not be shown again."}
          </p>
          <Field
            name="webhookUrl"
            label="Webhook URL"
            type="url"
            autoComplete="off"
            required={!settings.webhookEnabled}
          />
          <Field
            name="webhookSecret"
            label="Webhook signing secret (32–256 characters)"
            type="password"
            autoComplete="new-password"
            minLength={32}
            required={!settings.webhookEnabled}
          />
        </>
      )}
      <BusyButton busy={action.isPending}>
        Save notification delivery
      </BusyButton>
    </Form>
  );
}
