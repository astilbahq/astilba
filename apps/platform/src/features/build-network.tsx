import { useQuery, useQueryClient } from "@tanstack/react-query";
import { client, result, type Schema } from "../api/client";
import { Label } from "../components/label";
import { Textarea } from "../components/textarea";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Form, text, useAction } from "../ui";

const entry = /^(\d{1,3}(?:\.\d{1,3}){3}(?:\/\d{1,2})?):(\d{1,5})$/;

// One entry per line: an IPv4 address or range, a colon and a TCP port. The server checks
// what each entry may reach; this names the line whose form it can't read.
export function parseDestinations(value: string) {
  const lines = value
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
  if (lines.length > 32)
    throw new Error("Allow at most 32 private destinations.");
  return lines.map((line, index) => {
    const match = entry.exec(line);
    const port = Number(match?.[2]);
    if (!match || port < 1 || port > 65535)
      throw new Error(
        `Line ${index + 1}, “${line}”: write an IPv4 address or range and a TCP port from 1 to 65535, such as 10.0.4.12:443.`,
      );
    return { prefix: match[1], port };
  });
}

function formatDestination(d: Schema["BuildDestination"]) {
  return `${d.prefix.endsWith("/32") ? d.prefix.slice(0, -3) : d.prefix}:${d.port}`;
}

export function BuildNetwork({
  workspaceId,
  server,
  owner,
}: {
  workspaceId: string;
  server: Schema["Server"];
  owner: boolean;
}) {
  const action = useAction();
  const queries = useQueryClient();
  const path = { workspaceId, serverId: server.id };
  const queryKey = ["server-build-network", workspaceId, server.id];
  const query = useQuery({
    queryKey,
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/servers/{serverId}/build-network",
          { params: { path } },
        ),
      ),
  });
  const allow = query.data?.allow;
  return (
    <details>
      <summary>Build network</summary>
      <div className={s.stack}>
        <ErrorNotice error={query.error} />
        <p>
          Builds on this server reach public addresses only. They can’t reach
          this host, other containers, private or link-local addresses, cloud
          instance metadata or IPv6. The build’s own Git provider and registry
          are reachable even when they are private.
        </p>
        {allow && (
          <p>
            {allow.length
              ? `Also allowed: ${allow.map(formatDestination).join(", ")}.`
              : "No private destinations are allowed."}
          </p>
        )}
        {owner && allow && (
          <Form
            label={`Build network for ${server.name}`}
            action={action}
            onSubmit={(d) =>
              action.run(async () => {
                try {
                  await result(
                    client.PUT(
                      "/api/v1/workspaces/{workspaceId}/servers/{serverId}/build-network",
                      {
                        params: { path },
                        body: {
                          allow: parseDestinations(text(d, "allow")),
                        },
                      },
                    ),
                  );
                } finally {
                  await queries.invalidateQueries({ queryKey });
                }
              })
            }
          >
            <ErrorNotice error={action.error} />
            <Label>
              Allowed private destinations
              <Textarea
                name="allow"
                rows={4}
                defaultValue={allow.map(formatDestination).join("\n")}
                disabled={action.isPending}
                autoComplete="off"
                spellCheck={false}
                placeholder={"10.0.4.12:443\n10.0.5.0/24:8081"}
              />
            </Label>
            <p className={s.muted}>
              One per line, such as a private package mirror on your network: an
              IPv4 address or range and a TCP port, up to 32. This server and
              its containers can’t be allowed. Any build on this server,
              including code from its dependencies, can reach the destinations
              you allow. Changes are audited. Removing one stops builds queued
              before the change.
            </p>
            <BusyButton
              busy={action.isPending}
              disabled={server.transferRequired}
            >
              Save build network
            </BusyButton>
          </Form>
        )}
      </div>
    </details>
  );
}
