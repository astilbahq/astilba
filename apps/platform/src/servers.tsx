import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { client, result, type Schema } from "./api/client";
import { sessionOptions } from "./api/session";
import { Button } from "./components/button";
import { Input } from "./components/input";
import { Label } from "./components/label";
import { Anchor } from "./components/link";
import { Select } from "./components/select";
import { ServerMetrics } from "./features/metrics";
import { ServerDomains } from "./features/server-domains";
import { ServerOnboarding } from "./features/server-onboarding";
import * as s from "./styles";
import { BusyButton, ErrorNotice, Form, text, useAction } from "./ui";

export function Servers() {
  const session = useQuery(sessionOptions).data;
  const [selected, setSelected] = useState("");
  const workspace =
    session?.workspaces.find((w) => w.id === selected) ??
    session?.workspaces[0];
  const workspaceId = workspace?.id ?? "";
  return (
    <div className={s.stack}>
      <header>
        <p className={s.muted}>YOUR INFRASTRUCTURE</p>
        <h1>Servers</h1>
        <p>
          Connect your machines. Keep your workloads running independently of
          Astilba.
        </p>
      </header>
      <Label>
        Workspace
        <Select
          value={workspaceId}
          onChange={(e) => setSelected(e.target.value)}
        >
          {session?.workspaces.map((w) => (
            <option key={w.id} value={w.id}>
              {w.name}
            </option>
          ))}
        </Select>
      </Label>
      {workspace && <ServerWorkspace key={workspaceId} workspace={workspace} />}
    </div>
  );
}
function ServerWorkspace({ workspace }: { workspace: Schema["Workspace"] }) {
  const workspaceId = workspace.id;
  const queries = useQueryClient();
  const action = useAction();
  const [enrollment, setEnrollment] = useState<Schema["Enrollment"]>();
  useEffect(() => {
    if (!enrollment) return;
    const timer = setTimeout(
      () => setEnrollment(undefined),
      Math.max(0, new Date(enrollment.expiresAt).getTime() - Date.now()),
    );
    return () => clearTimeout(timer);
  }, [enrollment]);
  const [transfer, setTransfer] = useState<Schema["TransferTicket"]>();
  const [plan, setPlan] = useState<Schema["ExecutionPlan"]>();
  const [applyKey, setApplyKey] = useState("");
  const [projectId, setProject] = useState("");
  const editable = workspace.role !== "viewer";
  const owner = workspace.role === "owner";
  const path = { workspaceId };
  const servers = useQuery({
    queryKey: ["servers", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/servers", {
          params: { path },
        }),
      ),
    refetchInterval: 5000,
  });
  const operations = useQuery({
    queryKey: ["operations", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/operations", {
          params: { path },
        }),
      ),
    refetchInterval: 3000,
  });
  const releases = useQuery({
    queryKey: ["agent-releases", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/agent-releases", {
          params: { path },
        }),
      ),
  });
  const projects = useQuery({
    queryKey: ["projects", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/projects", {
          params: { path },
        }),
      ),
  });
  const selectedProject =
    projects.data?.find((p) => p.id === projectId)?.id ??
    projects.data?.[0]?.id ??
    "";
  const environments = useQuery({
    queryKey: ["environments", workspaceId, selectedProject],
    enabled: !!selectedProject,
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments",
          { params: { path: { workspaceId, projectId: selectedProject } } },
        ),
      ),
  });
  const refresh = async () => {
    await queries.invalidateQueries({ queryKey: ["servers", workspaceId] });
    await queries.invalidateQueries({ queryKey: ["operations", workspaceId] });
  };
  const run = (fn: () => Promise<unknown>) =>
    action.run(async () => {
      await fn();
      await refresh();
    });
  return (
    <>
      <ErrorNotice
        error={
          action.error ||
          servers.error ||
          operations.error ||
          projects.error ||
          environments.error ||
          releases.error
        }
      />
      {owner && (
        <ServerOnboarding
          workspaceId={workspaceId}
          releases={releases.data ?? []}
          onEnrollment={(value) => {
            setEnrollment(value);
            void refresh();
          }}
        />
      )}
      {enrollment && (
        <section className={s.panel} aria-label="Enrollment instructions">
          <h2>Connect {enrollment.server.name}</h2>
          <p>
            One-use code, expires{" "}
            {new Date(enrollment.expiresAt).toLocaleTimeString()}. Paste it into
            the agent’s stdin prompt; keep it out of shell history.
          </p>
          <Label>
            Enrollment code
            <Input readOnly value={enrollment.token} autoComplete="off" />
          </Label>
          <code className={s.code}>
            sudo astilba-agent enroll --management {location.origin} --server{" "}
            {enrollment.server.id} --release-key /etc/astilba/release-public.pem
          </code>
          <code className={s.code}>
            sudo systemctl enable --now astilba-agent
          </code>
          <p>
            Enrolling does not start the service. Enable it explicitly, then
            inspect the connection and role capabilities below.
          </p>
          <p>
            Use the signed download and systemd installation steps in the
            repository’s agent guide before running this command.
          </p>
          {releases.data?.length ? (
            <ul>
              {releases.data.map((release) => (
                <li key={release.sha256}>
                  {release.version} · Linux {release.arch}:{" "}
                  <Anchor
                    href={`/agent-artifacts/${release.sha256}`}
                    download="astilba-agent"
                  >
                    Download agent
                  </Anchor>
                  {" · "}
                  <Anchor
                    href={`/agent-artifacts/${release.sha256}/manifest.json`}
                    download="release.json"
                  >
                    Signed manifest
                  </Anchor>
                </li>
              ))}
            </ul>
          ) : (
            <p>
              No signed downloads are configured. The installation operator must
              provide the agent artifact and its signed manifest.
            </p>
          )}
          <Anchor
            href="https://github.com/astilbahq/astilba/blob/main/docs/agent.md"
            target="_blank"
            rel="noreferrer"
          >
            Agent installation guide
          </Anchor>
          <Button type="button" onClick={() => setEnrollment(undefined)}>
            Hide enrollment code
          </Button>
        </section>
      )}
      {transfer && (
        <section className={s.panel}>
          <h2>Confirm transfer on the host</h2>
          <p>
            Stop the agent service, save this one-use code in a private file on
            the host, and verify the target release key. The agent refuses
            transfer until unfinished operations are resolved. Workloads and
            stored data remain in place.
          </p>
          <code className={s.code}>{transfer.token}</code>
          <p>Expires {new Date(transfer.expiresAt).toLocaleString()}.</p>
          <code className={s.code}>
            astilba-agent transfer --management {transfer.managementOrigin}{" "}
            --server {transfer.serverId} --transfer-generation{" "}
            {transfer.generation} --token-file /root/astilba-transfer-token
            --release-key /etc/astilba/release-public.pem
          </code>
          <p>
            Restart the agent after the command saves the transfer. It retries
            enrollment if management is temporarily unavailable while installed
            schedules keep running. Confirm the server is connected here before
            making further changes. If the code expires, prepare another ticket
            and repeat the command with the new code.
          </p>
          <Button type="button" onClick={() => setTransfer(undefined)}>
            Hide transfer code
          </Button>
        </section>
      )}
      <section className={s.stack} aria-label="Connected servers">
        {!servers.data?.length && <p>No servers enrolled yet.</p>}
        {servers.data?.map((server) => (
          <article className={s.panel} key={server.id}>
            <div className={s.spread}>
              <h2>{server.name}</h2>
              <span>
                {server.status} · {server.role}
                {server.previewOnly ? " · Preview only" : ""}
              </span>
            </div>
            <p className={s.muted}>
              Last contact:{" "}
              {server.lastSeen
                ? new Date(server.lastSeen).toLocaleString()
                : "Awaiting enrollment"}
              . Connectivity does not describe workload health.
            </p>
            <ServerMetrics workspaceId={workspaceId} serverId={server.id} />
            {owner && server.transferRequired && (
              <div className={s.stack}>
                <p>
                  This imported or recovered server is fenced until its host
                  owner confirms the new controller.
                </p>
                <Button
                  type="button"
                  disabled={action.isPending}
                  onClick={() =>
                    run(async () =>
                      setTransfer(
                        await result(
                          client.POST(
                            "/api/v1/workspaces/{workspaceId}/servers/{serverId}/transfer",
                            {
                              params: {
                                path: { workspaceId, serverId: server.id },
                              },
                            },
                          ),
                        ),
                      ),
                    )
                  }
                >
                  Prepare local transfer
                </Button>
              </div>
            )}
            {server.capabilities && (
              <p>
                Agent {server.capabilities.version} · {server.capabilities.arch}{" "}
                · protocol {server.capabilities.protocol}
              </p>
            )}
            {server.role !== "build" && server.status !== "revoked" && (
              <ServerDomains
                workspaceId={workspaceId}
                server={server}
                owner={owner}
              />
            )}
            {server.capabilities && (
              <p>
                Role capabilities:{" "}
                {server.role !== "build"
                  ? `deployment ${server.capabilities.visual ? "available" : "unavailable"} · `
                  : ""}
                {server.role !== "deployment"
                  ? `builds ${server.capabilities.build ? "available" : "unavailable"} · `
                  : ""}
                backups{" "}
                {server.capabilities.backups ? "available" : "unavailable"}.
                Inspect missing requirements before deploying.
              </p>
            )}
            {server.preflight && (
              <>
                <p>
                  Host: {server.preflight.osRelease} · Docker{" "}
                  {server.preflight.dockerVersion || "unavailable"} ·{" "}
                  {server.preflight.existingContainers} existing containers ·{" "}
                  {Math.round(server.preflight.freeBytes / 1024 ** 3)} GiB
                  available
                </p>
                <p>
                  {server.preflight.ready
                    ? "Ready for the test-container operation."
                    : "Resolve host preflight requirements before execution."}
                </p>
                <p>
                  Certificate contact:{" "}
                  {server.preflight.certificateContactConfigured === undefined
                    ? "not reported by this agent"
                    : server.preflight.certificateContactConfigured
                      ? "configured; certificate issuance still requires deployment"
                      : "missing; prepare it before managed HTTPS using the agent guide"}
                  .
                </p>
                {server.preflight.notes.map((note) => (
                  <p className={s.notice} key={note}>
                    {note}
                  </p>
                ))}
              </>
            )}
            {owner && (
              <div className={s.row}>
                <Button
                  type="button"
                  disabled={
                    action.isPending ||
                    server.status === "pending" ||
                    server.status === "revoked" ||
                    server.transferRequired
                  }
                  onClick={() =>
                    run(() =>
                      result(
                        client.POST(
                          "/api/v1/workspaces/{workspaceId}/servers/{serverId}/rotate",
                          {
                            params: {
                              path: { workspaceId, serverId: server.id },
                            },
                          },
                        ),
                      ),
                    )
                  }
                >
                  {server.rotationRequested
                    ? "Rotation requested"
                    : "Rotate credential"}
                </Button>
                <Button
                  type="button"
                  disabled={action.isPending}
                  onClick={() => {
                    if (
                      confirm(
                        `Disconnect ${server.name}? Existing workloads keep running. Re-enrollment requires resolving active operations.`,
                      )
                    )
                      run(() =>
                        result(
                          client.POST(
                            "/api/v1/workspaces/{workspaceId}/servers/{serverId}/revoke",
                            {
                              params: {
                                path: { workspaceId, serverId: server.id },
                              },
                            },
                          ),
                        ),
                      );
                  }}
                >
                  Revoke connection
                </Button>
                <Button
                  type="button"
                  disabled={action.isPending || server.transferRequired}
                  onClick={() =>
                    run(async () =>
                      setEnrollment(
                        await result(
                          client.POST(
                            "/api/v1/workspaces/{workspaceId}/servers/{serverId}/enrollment",
                            {
                              params: {
                                path: { workspaceId, serverId: server.id },
                              },
                            },
                          ),
                        ),
                      ),
                    )
                  }
                >
                  New enrollment code
                </Button>
              </div>
            )}
            {owner &&
              releases.data?.some(
                (r) =>
                  r.arch === server.capabilities?.arch &&
                  r.version !== server.capabilities.version,
              ) && (
                <Form
                  action={action}
                  onSubmit={(data) => {
                    if (
                      confirm(
                        `Update ${server.name} to ${text(data, "version")}? The update requires an idle agent and verifies the signed artifact.`,
                      )
                    )
                      run(() =>
                        result(
                          client.POST(
                            "/api/v1/workspaces/{workspaceId}/servers/{serverId}/update",
                            {
                              params: {
                                path: { workspaceId, serverId: server.id },
                              },
                              body: { version: text(data, "version") },
                            },
                          ),
                        ),
                      );
                  }}
                >
                  <Label>
                    Agent release
                    <Select name="version">
                      {releases.data
                        .filter(
                          (r) =>
                            r.arch === server.capabilities?.arch &&
                            r.version !== server.capabilities.version,
                        )
                        .map((r) => (
                          <option key={r.sha256}>{r.version}</option>
                        ))}
                    </Select>
                  </Label>
                  <BusyButton busy={action.isPending}>Update agent</BusyButton>
                </Form>
              )}
          </article>
        ))}
      </section>
      {editable && (
        <section className={s.panel}>
          <h2>Test server execution</h2>
          <p>
            Review and apply a small, isolated container operation. The probe
            has no network, ports or volumes. Starting it leaves it running
            until you explicitly remove it.
          </p>
          {projects.data?.length ? (
            <Form
              action={action}
              onSubmit={(data) =>
                run(async () => {
                  setPlan(
                    await result(
                      client.POST("/api/v1/workspaces/{workspaceId}/plans", {
                        params: { path },
                        body: {
                          serverId: text(data, "server"),
                          environmentId: text(data, "environment"),
                          action: text(data, "action") as Schema["TestAction"],
                        },
                      }),
                    ),
                  );
                  setApplyKey(crypto.randomUUID());
                })
              }
            >
              <Label>
                Project
                <Select
                  value={selectedProject}
                  onChange={(e) => {
                    setProject(e.target.value);
                    setPlan(undefined);
                  }}
                >
                  {projects.data.map((p) => (
                    <option value={p.id} key={p.id}>
                      {p.name}
                    </option>
                  ))}
                </Select>
              </Label>
              <Label>
                Environment
                <Select name="environment" required>
                  {environments.data?.map((e) => (
                    <option key={e.id} value={e.id}>
                      {e.name}
                    </option>
                  ))}
                </Select>
              </Label>
              <Label>
                Deployment server
                <Select name="server" required>
                  {servers.data
                    ?.filter(
                      (s) =>
                        s.role !== "build" &&
                        s.preflight?.ready &&
                        s.status !== "revoked",
                    )
                    .map((s) => (
                      <option key={s.id} value={s.id}>
                        {s.name}
                      </option>
                    ))}
                </Select>
              </Label>
              <Label>
                Operation
                <Select name="action">
                  <option value="start">Start probe</option>
                  <option value="inspect">Inspect probe</option>
                  <option value="remove">Remove probe</option>
                </Select>
              </Label>
              <BusyButton busy={action.isPending}>Review test plan</BusyButton>
            </Form>
          ) : (
            <p>Create a project and environment first.</p>
          )}
          {plan && (
            <section className={s.stack} aria-label="Plan review">
              <h3>Review: {plan.action} probe</h3>
              <p>
                Server {servers.data?.find((s) => s.id === plan.serverId)?.name}
                . Environment{" "}
                {
                  environments.data?.find((e) => e.id === plan.environmentId)
                    ?.name
                }
                .
              </p>
              <code className={s.code}>{plan.image}</code>
              <p>
                No existing application, network or volume will be removed. This
                plan expires at {new Date(plan.expiresAt).toLocaleTimeString()}.
              </p>
              <Button
                type="button"
                disabled={action.isPending}
                onClick={() =>
                  run(async () => {
                    await result(
                      client.POST(
                        "/api/v1/workspaces/{workspaceId}/plans/{planId}/apply",
                        {
                          params: { path: { workspaceId, planId: plan.id } },
                          body: { idempotencyKey: applyKey },
                        },
                      ),
                    );
                    setPlan(undefined);
                  })
                }
              >
                Apply reviewed plan
              </Button>
              <Button type="button" onClick={() => setPlan(undefined)}>
                Discard plan
              </Button>
            </section>
          )}
        </section>
      )}
      <section className={s.stack} aria-label="Operation history">
        <h2>Recent operations</h2>
        <p>
          Operations continue when this page closes. Cancellation stops at the
          next safe checkpoint and retains any created probe.
        </p>
        {!operations.data?.length && <p>No operations yet.</p>}
        {operations.data?.map((op) => (
          <article className={s.panel} key={op.id}>
            <div className={s.spread}>
              <h3>
                {op.kind === "update" ? "Agent update" : "Test container"}
              </h3>
              <span>
                {op.state.replaceAll("_", " ")}
                {op.cancelRequested ? " · cancellation requested" : ""}
              </span>
            </div>
            <p>{op.note || "Waiting for the agent to report."}</p>
            <small>
              {new Date(op.createdAt).toLocaleString()} · generation{" "}
              {op.generation}
            </small>
            {op.steps.length > 0 && (
              <ol>
                {op.steps.map((step) => (
                  <li key={step.name}>
                    {step.name}: {step.state}
                    {step.detail && ` — ${step.detail}`}
                  </li>
                ))}
              </ol>
            )}
            {editable &&
              !["succeeded", "failed", "cancelled"].includes(op.state) && (
                <Button
                  type="button"
                  disabled={action.isPending || op.cancelRequested}
                  onClick={() =>
                    run(() =>
                      result(
                        client.POST(
                          "/api/v1/workspaces/{workspaceId}/operations/{operationId}/cancel",
                          {
                            params: {
                              path: { workspaceId, operationId: op.id },
                            },
                          },
                        ),
                      ),
                    )
                  }
                >
                  Request cancellation
                </Button>
              )}
            {op.state === "needs_attention" && (
              <p>
                Inspect this host locally, then use{" "}
                <code>astilba-agent retry --operation {op.id}</code> with the
                agent service stopped. Resume the service to reconcile the same
                operation.
              </p>
            )}
          </article>
        ))}
      </section>
    </>
  );
}
