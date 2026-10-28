import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Anchor } from "../components/link";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Form, useAction } from "../ui";

export const networkOptions = (workspaceId: string) => ({
  queryKey: ["managed-network", workspaceId],
  queryFn: () =>
    result(
      client.GET("/api/v1/workspaces/{workspaceId}/network", {
        params: { path: { workspaceId } },
      }),
    ),
  refetchInterval: 5000,
});

export function ManagedNetwork({
  workspaceId,
  servers,
  owner,
}: {
  workspaceId: string;
  servers: Schema["Server"][];
  owner: boolean;
}) {
  const network = useQuery(networkOptions(workspaceId));
  const queries = useQueryClient();
  const action = useAction();
  const [draft, setDraft] = useState<Schema["ConfigureManagedNetwork"]>();
  const [notice, setNotice] = useState("");
  const eligible = servers.filter(
    (server) =>
      !server.previewOnly &&
      server.role !== "build" &&
      server.capabilities?.crossHost,
  );
  const update = (
    serverId: string,
    value: Partial<Schema["ManagedNetworkMemberInput"]>,
  ) =>
    setDraft((current) =>
      current
        ? {
            ...current,
            members: current.members.map((member) =>
              member.serverId === serverId ? { ...member, ...value } : member,
            ),
          }
        : current,
    );
  return (
    <section className={s.panel} aria-label="Managed private network">
      <h2>Private network</h2>
      <p>
        Connect deployment servers directly. Application traffic stays on your
        servers. Installed links keep working during a management outage.
      </p>
      <ErrorNotice error={network.error ?? action.error} />
      {notice && <p role="status">{notice}</p>}
      {network.isPending && <p role="status">Loading network configuration…</p>}
      {network.data && (
        <>
          <p>
            {network.data.state === "unconfigured"
              ? "No private network configured."
              : network.data.state === "ready"
                ? "Configuration acknowledged. Each deployment separately checks endpoint reachability."
                : "Configuration changes await host acknowledgement. Revocation is incomplete until the affected peers acknowledge it."}
          </p>
          <ul>
            {network.data.members.map((member) => (
              <li key={member.serverId}>
                <strong>
                  {servers.find((server) => server.id === member.serverId)
                    ?.name ?? member.serverId}
                </strong>
                :{" "}
                {member.enabled
                  ? member.state === "ready"
                    ? "configured"
                    : "awaiting acknowledgement"
                  : member.state === "revocation_pending"
                    ? "removal awaits host acknowledgement"
                    : "removed"}{" "}
                · {member.address} ·{" "}
                {servers.find((server) => server.id === member.serverId)
                  ?.status ?? "unknown host state"}
                {member.note && <p>{member.note}</p>}
              </li>
            ))}
          </ul>
          <p>
            Disconnected hosts can retain their previous permissions. A
            configured link does not establish application health.
          </p>
          {owner && !draft && (
            <Button
              type="button"
              onClick={() => {
                setNotice("");
                setDraft({
                  baseGeneration: network.data.generation,
                  prefix: network.data.prefix || "10.251.0.0/24",
                  members: network.data.members
                    .filter((member) => member.enabled)
                    .map(
                      ({
                        serverId,
                        endpoint,
                        listenPort,
                        cpuMillis,
                        memoryMiB,
                        unboundedInventoryHash,
                      }) => ({
                        serverId,
                        endpoint,
                        listenPort,
                        cpuMillis,
                        memoryMiB,
                        unboundedInventoryHash,
                      }),
                    ),
                });
              }}
            >
              Configure private network
            </Button>
          )}
        </>
      )}
      {owner && (
        <details>
          <summary>Prepare a deployment server</summary>
          <p>
            Install the current signed agent, download and inspect this optional
            preparation script, then run its check and apply commands on the
            server. It installs pinned WireGuard and firewall tools and grants
            the agent the network permission it needs. Restart the agent while
            it is idle.
          </p>
          <Anchor
            href="/onboarding/network-prepare.sh"
            download="astilba-network-prepare.sh"
          >
            Download network preparation script
          </Anchor>
          <pre className={`${s.code} ${s.wrappedCode}`}>
            sudo bash astilba-network-prepare.sh --check{"\n"}sudo bash
            astilba-network-prepare.sh --apply
          </pre>
          <p>
            Allow the chosen UDP listener through the server provider’s
            firewall. Direct reachability is required; Astilba does not relay
            traffic.
          </p>
        </details>
      )}
      {draft && (
        <Form
          action={action}
          label="Configure private network"
          onSubmit={() =>
            action.run(async () => {
              const next = await result(
                client.PUT("/api/v1/workspaces/{workspaceId}/network", {
                  params: { path: { workspaceId } },
                  body: draft,
                }),
              );
              queries.setQueryData(networkOptions(workspaceId).queryKey, next);
              setDraft(undefined);
              setNotice(
                "Network changes saved. Waiting for host acknowledgements.",
              );
            })
          }
        >
          <Label>
            Private IPv4 prefix
            <Input
              value={draft.prefix}
              required
              onChange={(event) =>
                setDraft({ ...draft, prefix: event.target.value })
              }
              disabled={draft.baseGeneration > 0}
            />
          </Label>
          <p>
            Choose an unused private /20–/24 prefix. The prefix stays fixed
            after creation. Existing routes are checked on each host before
            setup.
          </p>
          {servers
            .filter(
              (server) =>
                eligible.some((candidate) => candidate.id === server.id) ||
                draft.members.some((member) => member.serverId === server.id),
            )
            .map((server) => {
              const member = draft.members.find(
                (value) => value.serverId === server.id,
              );
              const inventory = server.preflight?.crossHostInventory;
              return (
                <fieldset key={server.id} className={s.stack}>
                  <legend>{server.name}</legend>
                  <Label>
                    <Checkbox
                      checked={!!member}
                      disabled={!member && !inventory}
                      onChange={(event) => {
                        setDraft({
                          ...draft,
                          members:
                            event.target.checked && inventory
                              ? [
                                  ...draft.members,
                                  {
                                    serverId: server.id,
                                    endpoint: "",
                                    listenPort: 51820,
                                    cpuMillis: Math.max(
                                      100,
                                      Math.floor(inventory.cpuMillis / 2),
                                    ),
                                    memoryMiB: Math.max(
                                      64,
                                      Math.floor(inventory.memoryMiB / 2),
                                    ),
                                    unboundedInventoryHash:
                                      inventory.unboundedInventoryHash,
                                  },
                                ]
                              : draft.members.filter(
                                  (value) => value.serverId !== server.id,
                                ),
                        });
                      }}
                    />
                    Include {server.name}
                  </Label>
                  {member && (
                    <>
                      <Label>
                        Reachable UDP endpoint
                        <Input
                          value={member.endpoint}
                          required
                          placeholder="192.0.2.10:51820"
                          onChange={(event) =>
                            update(server.id, { endpoint: event.target.value })
                          }
                        />
                      </Label>
                      <Label>
                        UDP listener port
                        <Input
                          type="number"
                          min={1024}
                          max={65535}
                          value={member.listenPort}
                          required
                          onChange={(event) =>
                            update(server.id, {
                              listenPort: Number(event.target.value),
                            })
                          }
                        />
                      </Label>
                      <Label>
                        Allocatable CPU (millicores)
                        <Input
                          type="number"
                          min={100}
                          max={inventory ? inventory.cpuMillis - 1 : 256000}
                          value={member.cpuMillis}
                          required
                          onChange={(event) =>
                            update(server.id, {
                              cpuMillis: Number(event.target.value),
                            })
                          }
                        />
                      </Label>
                      <Label>
                        Allocatable memory (MiB)
                        <Input
                          type="number"
                          min={64}
                          max={inventory ? inventory.memoryMiB - 1 : 1048576}
                          value={member.memoryMiB}
                          required
                          onChange={(event) =>
                            update(server.id, {
                              memoryMiB: Number(event.target.value),
                            })
                          }
                        />
                      </Label>
                      <p>
                        Keep a reserve for the operating system, ingress,
                        unmanaged services and existing services without
                        enforced CPU or memory limits. Replacement pools must
                        fit alongside the serving pools.
                      </p>
                      {inventory &&
                        member.unboundedInventoryHash !==
                          inventory.unboundedInventoryHash && (
                          <p role="alert">
                            Unbounded workloads changed since this capacity
                            acknowledgement.
                          </p>
                        )}
                      {inventory && (
                        <Button
                          type="button"
                          onClick={() =>
                            update(server.id, {
                              unboundedInventoryHash:
                                inventory.unboundedInventoryHash,
                            })
                          }
                        >
                          Acknowledge current workload inventory for{" "}
                          {server.name}
                        </Button>
                      )}
                    </>
                  )}
                </fieldset>
              );
            })}
          {eligible.length === 0 && (
            <p>
              No eligible servers. Complete network preparation on a deployment
              server first.
            </p>
          )}
          <Label>
            <Checkbox name="capacityReviewed" required />I have reserved
            capacity for existing unbounded workloads and reviewed the network
            and firewall changes. Removed peers may keep their prior permissions
            until reachable members acknowledge removal.
          </Label>
          <p>
            Saving requires owner access and a recent identity confirmation in
            Account security.
          </p>
          <div className={s.row}>
            <BusyButton busy={action.isPending}>
              Save private network
            </BusyButton>
            <Button type="button" onClick={() => setDraft(undefined)}>
              Cancel network changes
            </Button>
          </div>
        </Form>
      )}
    </section>
  );
}
