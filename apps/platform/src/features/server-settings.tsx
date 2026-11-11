import type { Schema } from "../api/client";
import { BuildNetwork } from "./build-network";
import { ServerDomains } from "./server-domains";

// The per-server settings an owner reviews: where traffic reaches the server, and where its
// builds may reach.
export function ServerSettings({
  workspaceId,
  server,
  owner,
}: {
  workspaceId: string;
  server: Schema["Server"];
  owner: boolean;
}) {
  if (server.status === "revoked") return null;
  return (
    <>
      {server.role !== "build" && (
        <ServerDomains
          workspaceId={workspaceId}
          server={server}
          owner={owner}
        />
      )}
      {server.role !== "deployment" && (
        <BuildNetwork workspaceId={workspaceId} server={server} owner={owner} />
      )}
    </>
  );
}
