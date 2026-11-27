import { useState } from "react";
import type { Schema } from "../api/client";
import { AgentUpdate } from "../features/agent-update";
import { useAction } from "../ui";

// Fixtures for the agent update stories: where the select starts, and how it keeps its release
// while the page is open.
export const release = (
  version: string,
  minimumVersion: string,
  arch: Schema["AgentRelease"]["arch"] = "arm64",
): Schema["AgentRelease"] => ({
  version,
  minimumVersion,
  arch,
  protocol: 1,
  size: 1,
  sha256: `${version}-${arch}`.padEnd(64, "0"),
  commit: "0".repeat(40),
  signature: "",
});
// Management lists the catalog newest first.
export const releases = [
  release("0.3.1", "0.0.0", "amd64"),
  release("0.3.0", "0.3.0"),
  release("0.2.1", "0.1.0"),
  release("0.2.0", "0.1.0"),
  release("0.1.0", "0.0.0"),
];
export const server = (
  version: string,
  agentUpdates?: Schema["AgentUpdates"],
): Schema["Server"] => ({
  id: "00000000-0000-4000-8000-000000000330",
  name: "edge-1",
  role: "deployment",
  status: "online",
  lastSeen: "2026-11-26T09:00:00Z",
  rotationRequested: false,
  preflight: null,
  capabilities: {
    protocol: 1,
    version,
    os: "linux",
    arch: "arm64",
    docker: true,
  },
  agentUpdates,
});

// The panel sits in a server's card on the Servers page, and reports the release it would send.
// later stands in for the page's next refreshes of the server, each after its agent changed, and
// laterCatalog for the catalog after management restarted with another one.
export function ServerCard({
  server,
  later = [],
  catalog = releases,
  laterCatalog = catalog,
}: {
  server: Schema["Server"];
  later?: Schema["Server"][];
  catalog?: Schema["AgentRelease"][];
  laterCatalog?: Schema["AgentRelease"][];
}) {
  const action = useAction();
  const [sent, setSent] = useState<string>();
  const [refreshes, setRefreshes] = useState(0);
  return (
    <main>
      <h1>Servers</h1>
      <article aria-label={server.name}>
        <h2>{server.name}</h2>
        <AgentUpdate
          server={refreshes ? later[refreshes - 1] : server}
          releases={refreshes ? laterCatalog : catalog}
          action={action}
          busy={false}
          onUpdate={setSent}
        />
        {refreshes < later.length && (
          <button type="button" onClick={() => setRefreshes(refreshes + 1)}>
            Refresh the server
          </button>
        )}
        {sent !== undefined && <p role="status">Requested {sent}</p>}
      </article>
    </main>
  );
}

// Releases that share a floor of 0.1.0, after one older release.
export const sameFloor = [
  release("0.4.0", "0.1.0"),
  release("0.3.0", "0.1.0"),
  release("0.2.0", "0.1.0"),
  release("0.1.0", "0.0.0"),
];
