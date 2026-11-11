import { client, result, type Schema } from "../api/client";
import { Button } from "../components/button";
import * as s from "../styles";

// An owner's controls for a server's agent credential: routine rotation, revocation and a new
// enrollment code.
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
  const rotatable = server.capabilities?.machineBoundRotation === true;
  const leak =
    "If you suspect a leak, revoke the connection, then add a new server, enroll a rebuilt host or a fresh state directory as that server, and move the work across. Renewing this server with the same state directory sends the leaked credential again, and renewing it with a new machine is refused.";
  return (
    <>
      {server.status !== "pending" && server.capabilities && (
        <p className={s.muted}>
          {server.status === "revoked"
            ? leak
            : rotatable
              ? `Rotating is routine upkeep, not a response to a leak: whoever holds a copy of this server's state directory can finish the rotation first. ${leak}`
              : `This server's agent can't rotate its credential safely; update it to rotate. ${leak}`}
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
            !rotatable
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
          {server.rotationRequested
            ? "Rotation requested"
            : "Rotate credential (routine)"}
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
      </div>
    </>
  );
}
