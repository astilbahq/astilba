import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { client, result, type Schema } from "../api/client";
import { volumesOptions } from "../api/environment";
import { Checkbox } from "../components/checkbox";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { ErrorNotice } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

// Choosing where to deploy, and, when the environment's primary was revoked, accepting what a
// replacement leaves behind. Management refuses a plan unless every volume on the revoked primary
// is named, so the list is rebuilt, unticked, whenever it changes.
export function DeploymentTarget({
  path,
  servers,
  specification,
  serverId,
  setServerId,
  owner,
}: {
  path: EnvironmentPath;
  servers: Schema["Server"][] | undefined;
  specification: Schema["Specification"];
  serverId: string;
  setServerId: (id: string) => void;
  owner: boolean;
}) {
  const compose = !!specification.compose;
  const declared = specification.volumes?.map((volume) => volume.id) ?? [];
  // The deployment panel already loads this list, so the query is shared.
  const volumes = useQuery(volumesOptions(path)).data;
  const replacement = useQuery({
    queryKey: ["primary-replacement", path],
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/primary-replacement",
          { params: { path } },
        ),
      ),
    refetchInterval: 5000,
  });
  const view = replacement.data;
  const continueOn = view?.continueOnServerId;
  useEffect(() => {
    if (continueOn && serverId !== continueOn) setServerId(continueOn);
  }, [continueOn, serverId, setServerId]);
  const name = (id: string | undefined) =>
    servers?.find((server) => server.id === id)?.name ?? id ?? "";
  const volumeName = (id: string) =>
    specification.volumes?.find((volume) => volume.id === id)?.name ??
    volumes?.find((volume) => volume.id === id)?.name ??
    `Volume ${id.slice(0, 8)}`;
  const abandoned = view?.abandonedVolumeIds ?? [];
  return (
    <>
      <ErrorNotice error={replacement.error} />
      <Label>
        Deployment server
        <Select
          value={serverId}
          onChange={(e) => setServerId(e.target.value)}
          disabled={!!continueOn}
          aria-describedby={continueOn ? "replacement-continue" : undefined}
          required
        >
          <option value="">Choose an enrolled server</option>
          {servers
            ?.filter((v) => v.role !== "build" && v.status !== "revoked")
            .map((v) => (
              <option
                key={v.id}
                value={v.id}
                disabled={
                  !v.capabilities?.visual ||
                  (compose && !v.capabilities?.compose)
                }
              >
                {v.name}
                {v.capabilities?.visual && (!compose || v.capabilities?.compose)
                  ? ""
                  : " — supported Compose required"}
              </option>
            ))}
        </Select>
      </Label>
      {view?.required && (
        <fieldset
          className={s.stack}
          disabled={!owner}
          aria-describedby="replacement-warning"
        >
          <legend>Replace the revoked server</legend>
          <input type="hidden" name="replacement" value="yes" />
          <p id="replacement-warning">
            {name(view.revokedServerId)} was revoked, and this
            environment&apos;s primary was on it. Deploying makes the server you
            choose the primary instead. Nothing is copied from{" "}
            {name(view.revokedServerId)}: it keeps its data, secrets, registry
            access and TLS keys, and it may keep serving at its own address.
            Rotate them, point custom domains at the new server and change
            generated ones, which name their server. Approve the host
            permissions below as well.
          </p>
          {continueOn && (
            <p id="replacement-continue">
              An earlier replacement started on {name(continueOn)}, so it
              continues there.
            </p>
          )}
          {!owner && (
            <p>Only a workspace owner can replace a revoked server.</p>
          )}
          {abandoned.length > 0 ? (
            <div className={s.stack} key={abandoned.join()}>
              <p>
                Accept each volume left behind. Restore its data from a snapshot
                by hand afterwards. Backups stored on{" "}
                {name(view.revokedServerId)} are lost with it.
              </p>
              {abandoned.map((id) => (
                <Label key={id} className={s.row}>
                  <Checkbox name="abandonedVolume" value={id} required />
                  {declared.includes(id)
                    ? `Start ${volumeName(id)} empty`
                    : `Drop ${volumeName(id)}`}
                </Label>
              ))}
            </div>
          ) : (
            <p className={s.muted}>This environment has no volumes to lose.</p>
          )}
        </fieldset>
      )}
    </>
  );
}
