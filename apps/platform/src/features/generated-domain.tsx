import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Form, text, useAction } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

export function GeneratedDomain({
  path,
  spec,
  change,
}: {
  path: EnvironmentPath;
  spec: Schema["Specification"];
  change: (v: Schema["Specification"]) => void;
}) {
  const action = useAction();
  const [candidate, setCandidate] = useState<{
    domain: Schema["DomainSuggestion"];
    app: string;
    port: number;
  }>();
  const servers = useQuery({
    queryKey: ["servers", path.workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/servers", {
          params: { path },
        }),
      ),
  });
  return (
    <details>
      <summary>Generate an application domain</summary>
      <div className={s.stack}>
        <p>
          Configure the server’s application-domain addresses on Servers first.
          Save new services before requesting a name. Choose the same server
          when planning deployment; moving a generated route requires explicit
          regeneration.
        </p>
        <ErrorNotice error={servers.error || action.error} />
        <Form
          action={action}
          label="Generated application domain"
          onSubmit={(d) =>
            action.run(async () => {
              setCandidate(undefined);
              const app = text(d, "applicationId");
              const port = Number(text(d, "port"));
              const domain = await result(
                client.POST(
                  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/domain-suggestion",
                  {
                    params: { path },
                    body: { applicationId: app, serverId: text(d, "serverId") },
                  },
                ),
              );
              setCandidate({ domain, app, port });
            })
          }
        >
          <label>
            Service
            <select name="applicationId" required disabled={action.isPending}>
              {spec.applications
                .filter((a) => !a.kind || a.kind === "web")
                .map((a) => (
                  <option value={a.id} key={a.id}>
                    {a.name}
                  </option>
                ))}
            </select>
          </label>
          <label>
            Generated domain server
            <select name="serverId" required disabled={action.isPending}>
              <option value="">Choose a server</option>
              {servers.data
                ?.filter(
                  (s) =>
                    s.role !== "build" &&
                    s.status !== "revoked" &&
                    !s.transferRequired,
                )
                .map((s) => (
                  <option value={s.id} key={s.id}>
                    {s.name}
                  </option>
                ))}
            </select>
          </label>
          <label>
            App port
            <input
              name="port"
              type="number"
              min={1}
              max={65535}
              defaultValue={8080}
              required
              disabled={action.isPending}
            />
          </label>
          <BusyButton busy={action.isPending}>
            Prepare generated route
          </BusyButton>
        </Form>
        {candidate && (
          <div className={s.stack} role="status">
            <p>
              <code className={s.code}>{candidate.domain.hostname}</code> →{" "}
              {spec.applications.find((a) => a.id === candidate.app)?.name} ·
              port {candidate.port} · server{" "}
              {servers.data?.find((s) => s.id === candidate.domain.serverId)
                ?.name ?? candidate.domain.serverId}
              .
            </p>
            <p>
              DNS configuration: {candidate.domain.dns.state}. Managed
              certificates require an approved ingress plan and public ports
              80/443.
            </p>
            <button
              type="button"
              disabled={!spec.applications.some((a) => a.id === candidate.app)}
              onClick={() => {
                change({
                  ...spec,
                  routes: [
                    ...(spec.routes ?? []),
                    {
                      id: crypto.randomUUID(),
                      targetId: candidate.app,
                      domain: candidate.domain.hostname,
                      protocol: "http",
                      port: candidate.port,
                      listenPort: 0,
                      tls: "managed",
                      allowCidrs: [],
                    },
                  ],
                });
                setCandidate(undefined);
              }}
            >
              Stage generated HTTP route
            </button>
          </div>
        )}
      </div>
    </details>
  );
}
