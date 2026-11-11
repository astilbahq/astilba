import { client, result, type Schema } from "../api/client";
import { Button } from "../components/button";
import * as s from "../styles";

const protection: Record<string, string> = {
  tpm: "sealed to this machine's TPM; a copy of its disk can't use it",
  host_key:
    "sealed to this machine's systemd host key; a copy of its state directory can't use it, but a copy of its whole disk can",
  unsealed: "not sealed (development build)",
  setup_required:
    "not yet sealed: run `sudo systemd-creds setup` on the host once, then the agent registers a key",
};

const signals: Record<string, string> = {
  start_counter: "two copies of the agent started sessions with the same key",
  superseded_key: "a key the agent replaced was used after its replacement",
  retired_bearer:
    "the credential the agent replaced with its key was used again",
  bind_other_key: "the agent's old credential tried to register a second key",
  code_other_key: "the enrollment code was presented again with another key",
};

function age(at?: string | null) {
  if (!at) return "";
  const days = Math.floor((Date.now() - Date.parse(at)) / 86_400_000);
  return days < 1 ? "today" : days === 1 ? "1 day ago" : `${days} days ago`;
}

// An owner's controls for a server's agent identity: routine key rotation, revocation, removal
// from private networks, and a new enrollment code, which re-keys the host.
export function ServerCredentials({
  workspaceId,
  server,
  busy,
  run,
  onEnrollment,
}: {
  workspaceId: string;
  server: Schema["Server"];
  busy: boolean;
  run: (fn: () => Promise<unknown>) => void;
  onEnrollment: (enrollment: Schema["Enrollment"]) => void;
}) {
  const path = { workspaceId, serverId: server.id };
  const identity = server.identity;
  const keyed = identity?.keyBound === true;
  const kept = server.capabilities?.keyProtection;
  const leak = keyed
    ? "If you suspect its state directory leaked, create a new enrollment code and enroll this host again: it registers a new key, and every copy of the old one stops working. A copy also holds the server's private network key, so remove it from private networks too. If the host itself may be compromised, revoke it and enroll a rebuilt host as a new server."
    : "This agent still uses a bearer credential, which a copy of its state directory can use from anywhere. Update the agent: it then registers a key it holds. Until then, after a suspected leak, revoke the connection and enroll a rebuilt host or a fresh state directory as a new server.";
  return (
    <>
      {identity?.conflictAt && (
        <div className={s.notice} role="alert">
          <p>
            Another copy of this server's agent key is in use:{" "}
            {signals[identity.conflictSignal ?? ""] ??
              "management saw proof of a second holder"}
            , {age(identity.conflictAt)}. A copy of its state directory also
            holds its private network key, so remove it from private networks.
            Then create a new enrollment code and enroll this host again, or
            revoke it if the host itself may be compromised.
          </p>
        </div>
      )}
      {server.status !== "pending" && server.capabilities && (
        <p className={s.muted}>
          {keyed
            ? `The agent holds a key${kept ? `, ${protection[kept] ?? kept}` : ""}. Registered ${age(identity?.keyRotatedAt ?? identity?.keyCreatedAt)}; it is replaced every 30 days.${identity?.lineageStartedAt ? ` The agent's current session started ${age(identity.lineageStartedAt)}.` : ""} `
            : kept === "setup_required"
              ? `The agent's key is ${protection.setup_required}. `
              : ""}
          {leak}
        </p>
      )}
      <div className={s.row}>
        <Button
          type="button"
          disabled={
            busy ||
            server.status === "pending" ||
            server.status === "revoked" ||
            server.transferRequired ||
            !keyed
          }
          onClick={() =>
            run(() =>
              result(
                client.POST(
                  "/api/v1/workspaces/{workspaceId}/servers/{serverId}/rotate",
                  { params: { path } },
                ),
              ),
            )
          }
        >
          Rotate key now
        </Button>
        <Button
          type="button"
          disabled={busy}
          onClick={() => {
            if (
              confirm(
                `Revoke ${server.name}? Its credential stops working and its unfinished work ends. It may keep running what it was given and it keeps its secrets, registry access and TLS keys, so rotate them. Renewing it later doesn't undo this. ${leak} A host that held a reservation for work that has since ended may only come back through offline recovery, which isn't established.`,
              )
            )
              run(() =>
                result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/servers/{serverId}/revoke",
                    { params: { path } },
                  ),
                ),
              );
          }}
        >
          Revoke connection
        </Button>
        <Button
          type="button"
          disabled={busy || server.transferRequired}
          onClick={() =>
            run(async () =>
              onEnrollment(
                await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/servers/{serverId}/enrollment",
                    { params: { path } },
                  ),
                ),
              ),
            )
          }
        >
          New enrollment code
        </Button>
        <Button
          type="button"
          disabled={busy || server.status === "pending"}
          onClick={() => {
            if (
              confirm(
                `Remove ${server.name} from every private network? Its peers stop accepting its network key. Adding it back needs your approval, and it gets a new key.`,
              )
            )
              run(() =>
                result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/servers/{serverId}/network-removal",
                    { params: { path } },
                  ),
                ),
              );
          }}
        >
          Remove from private networks
        </Button>
      </div>
    </>
  );
}
