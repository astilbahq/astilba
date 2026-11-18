import { useQuery, useQueryClient } from "@tanstack/react-query";
import { client, result } from "../api/client";
import { volumesOptions } from "../api/environment";
import { Button } from "../components/button";
import * as s from "../styles";
import type { useAction } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;

/**
 * The environment's data volumes, which an owner may delete once nothing uses them. A delete that
 * hasn't ended shows as deleting: until it does, the server refuses any draft or plan that uses
 * the volume, and the refusal shows through `action`.
 */
export function ManagedVolumes({
  path,
  action,
  owner,
  dirty,
  compose,
  removeVolume,
  setMessage,
}: {
  path: EnvironmentPath;
  action: ReturnType<typeof useAction>;
  owner: boolean;
  dirty: boolean;
  compose: boolean;
  removeVolume: (id: string) => void;
  setMessage: (message: string) => void;
}) {
  const queries = useQueryClient();
  const volumes = useQuery({ ...volumesOptions(path), refetchInterval: 5000 });
  return (
    <ul className={s.list}>
      {volumes.data?.map((volume) => (
        <li key={volume.id}>
          {volume.name} ·{" "}
          {volume.deleted
            ? "Deleted"
            : volume.deleting
              ? "Deleting"
              : volume.inUse
                ? "Attached"
                : "Retained"}
          {owner && (
            <Button
              type="button"
              disabled={
                volume.inUse ||
                volume.deleted ||
                volume.deleting ||
                dirty ||
                action.isPending
              }
              onClick={() => {
                if (
                  window.confirm(
                    `Permanently delete all data in ${volume.name}? This cannot be undone.`,
                  )
                )
                  action.run(async () => {
                    await result(
                      client.DELETE(`${root}/volumes/{volumeId}`, {
                        params: { path: { ...path, volumeId: volume.id } },
                      }),
                    );
                    removeVolume(volume.id);
                    await queries.invalidateQueries({
                      queryKey: ["volumes", path],
                    });
                    setMessage(
                      compose
                        ? "Data-volume deletion queued. Until it ends, no draft or plan can use the volume. Check its result, then remove the unused volume declaration from your source files before deploying again."
                        : "Data-volume deletion queued and removal staged. Until it ends, no draft or plan can use the volume. Check the operation result, then save the configuration revision.",
                    );
                  });
              }}
            >
              Delete data volume permanently
            </Button>
          )}
        </li>
      ))}
    </ul>
  );
}
