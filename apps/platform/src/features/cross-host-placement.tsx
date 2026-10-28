import { useQuery } from "@tanstack/react-query";
import type { Schema } from "../api/client";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { ErrorNotice } from "../ui";
import { networkOptions } from "./managed-network";

type Spec = Schema["Specification"];
export function CrossHostPlacement({
  workspaceId,
  spec,
  change,
  servers,
}: {
  workspaceId: string;
  spec: Spec;
  change: (spec: Spec) => void;
  servers: Schema["Server"][];
}) {
  const network = useQuery(networkOptions(workspaceId));
  const placement = spec.placement;
  const members =
    network.data?.members.filter((member) => member.enabled) ?? [];
  const name = (id: string) =>
    servers.find((server) => server.id === id)?.name ?? id;
  const eligible = spec.applications.filter(
    (app) =>
      (app.kind ?? "web") === "web" &&
      !app.postgres &&
      !app.mounts?.length &&
      app.health &&
      spec.routes?.some(
        (route) => route.targetId === app.id && route.protocol === "http",
      ),
  );
  const stage = (value: Schema["CrossHostPlacement"]) =>
    change({ ...spec, placement: value });
  return (
    <details className={s.stack}>
      <summary>Application placement across servers</summary>
      <ErrorNotice error={network.error} />
      <p>
        Explicit placement keeps ingress, databases and persistent volumes on
        the primary server. It does not move data or provide automatic failover.
        Counts and limits are staged with this revision.
      </p>
      <p>
        Deploy the environment on its primary server first, with managed ingress
        enabled, before adding another server. Renew backup policies after a
        placement change so writer-stopping backups use the current group.
      </p>
      {!placement && (
        <Button
          type="button"
          disabled={
            !network.data ||
            network.data.state !== "ready" ||
            members.length === 0 ||
            eligible.length === 0
          }
          onClick={() => {
            if (!network.data || !members[0] || !eligible[0]) return;
            stage({
              networkId: network.data.id,
              ingressServerId: members[0].serverId,
              applications: [
                {
                  applicationId: eligible[0].id,
                  cpuMillis: 500,
                  memoryMiB: 256,
                  targets: [
                    {
                      serverId: members[0].serverId,
                      count: eligible[0].replicas ?? 1,
                    },
                  ],
                },
              ],
            });
          }}
        >
          Set explicit application placement
        </Button>
      )}
      {network.data?.state !== "ready" && (
        <p>
          Configure and acknowledge the private network in Servers before
          preparing a cross-host plan.
        </p>
      )}
      {placement && (
        <>
          <Label>
            Primary ingress server
            <Select
              value={placement.ingressServerId}
              onChange={(event) =>
                stage({ ...placement, ingressServerId: event.target.value })
              }
            >
              {!members.some(
                (member) => member.serverId === placement.ingressServerId,
              ) && (
                <option value={placement.ingressServerId}>
                  {name(placement.ingressServerId)} (unavailable)
                </option>
              )}
              {members.map((member) => (
                <option key={member.serverId} value={member.serverId}>
                  {name(member.serverId)}
                </option>
              ))}
            </Select>
          </Label>
          <p>
            An environment already deployed must keep its existing primary. Each
            application needs HTTP readiness on its routed port and must have no
            mounted volumes or downstream consumers.
          </p>
          {spec.applications.map((app) => {
            const group = placement.applications.find(
              (value) => value.applicationId === app.id,
            );
            const update = (
              value: Partial<
                Schema["CrossHostPlacement"]["applications"][number]
              >,
            ) =>
              stage({
                ...placement,
                applications: placement.applications.map((item) =>
                  item.applicationId === app.id ? { ...item, ...value } : item,
                ),
              });
            return (
              <fieldset key={app.id} className={s.stack}>
                <legend>{app.name}</legend>
                <Label>
                  <Checkbox
                    checked={!!group}
                    disabled={!group && !eligible.includes(app)}
                    onChange={(event) =>
                      stage({
                        ...placement,
                        applications: event.target.checked
                          ? [
                              ...placement.applications,
                              {
                                applicationId: app.id,
                                cpuMillis: 500,
                                memoryMiB: 256,
                                targets: [
                                  {
                                    serverId: placement.ingressServerId,
                                    count: app.replicas ?? 1,
                                  },
                                ],
                              },
                            ]
                          : placement.applications.filter(
                              (value) => value.applicationId !== app.id,
                            ),
                      })
                    }
                  />
                  Use explicit placement for {app.name}
                </Label>
                {group && (
                  <>
                    <Label>
                      CPU per instance (millicores)
                      <Input
                        type="number"
                        min={100}
                        max={16000}
                        value={group.cpuMillis}
                        onChange={(event) =>
                          update({ cpuMillis: Number(event.target.value) })
                        }
                      />
                    </Label>
                    <Label>
                      Memory per instance (MiB)
                      <Input
                        type="number"
                        min={64}
                        max={65536}
                        value={group.memoryMiB}
                        onChange={(event) =>
                          update({ memoryMiB: Number(event.target.value) })
                        }
                      />
                    </Label>
                    {Array.from(
                      new Set([
                        ...members.map((member) => member.serverId),
                        ...group.targets.map((target) => target.serverId),
                      ]),
                    ).map((serverId) => (
                      <Label key={serverId}>
                        {name(serverId)} instances
                        <Input
                          type="number"
                          min={0}
                          max={10}
                          value={
                            group.targets.find(
                              (target) => target.serverId === serverId,
                            )?.count ?? 0
                          }
                          onChange={(event) => {
                            const count = Number(event.target.value);
                            update({
                              targets: [
                                ...group.targets.filter(
                                  (target) => target.serverId !== serverId,
                                ),
                                ...(count > 0 ? [{ serverId, count }] : []),
                              ],
                            });
                          }}
                        />
                      </Label>
                    ))}
                    <p role="status">
                      {group.targets.reduce(
                        (total, target) => total + target.count,
                        0,
                      )}{" "}
                      placed of {app.replicas ?? 1} desired instances. The total
                      must match before saving.
                    </p>
                    <Button
                      type="button"
                      onClick={() =>
                        update({
                          targets: [
                            {
                              serverId: placement.ingressServerId,
                              count: app.replicas ?? 1,
                            },
                          ],
                        })
                      }
                    >
                      Consolidate {app.name} onto primary
                    </Button>
                  </>
                )}
              </fieldset>
            );
          })}
          <p>
            Save this revision, then prepare and review a deployment plan.
            Applying a network or placement change requires owner approval.
          </p>
        </>
      )}
    </details>
  );
}
