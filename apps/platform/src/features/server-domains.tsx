import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Form, text, useAction } from "../ui";

export function ServerDomains({
  workspaceId,
  server,
  owner,
}: {
  workspaceId: string;
  server: Schema["Server"];
  owner: boolean;
}) {
  const action = useAction();
  const path = { workspaceId, serverId: server.id };
  const query = useQuery({
    queryKey: ["server-domain", workspaceId, server.id],
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/servers/{serverId}/domain",
          { params: { path } },
        ),
      ),
    refetchInterval: 10000,
  });
  const [check, setCheck] = useState<Schema["DomainCheck"]>();
  const profile = query.data;
  return (
    <details>
      <summary>Application domains</summary>
      <div className={s.stack}>
        <ErrorNotice error={query.error || action.error} />
        {profile && (
          <>
            <p>
              DNS configuration: {profile.state.replaceAll("_", " ")}.{" "}
              {profile.note}
            </p>
            {profile.state !== "disabled" && (
              <>
                <p>
                  Wildcard: <code className={s.code}>{profile.wildcard}</code>.
                  Traffic goes directly to this server.{" "}
                  {profile.automaticDNS
                    ? "Astilba manages these records after owner approval."
                    : "Create the records below in your DNS provider."}
                </p>
                {profile.ipv4 && (
                  <p className={s.code}>
                    A · {profile.wildcard} → {profile.ipv4}
                  </p>
                )}
                {profile.ipv6 && (
                  <p className={s.code}>
                    AAAA · {profile.wildcard} → {profile.ipv6}
                  </p>
                )}
                <p className={s.muted}>
                  {profile.observedAt
                    ? `Provider last checked ${new Date(profile.observedAt).toLocaleString()}.`
                    : "Provider records have not been observed."}{" "}
                  DNS propagation, certificates and application health are
                  separate.
                </p>
                {owner && (
                  <DomainAddressForm
                    workspaceId={workspaceId}
                    server={server}
                    profile={profile}
                  />
                )}
              </>
            )}
          </>
        )}
        <h3>Use your own domain</h3>
        <p>
          Add an A/AAAA record pointing to this server (or a CNAME to an
          existing generated app name). Stage that hostname in the app’s HTTP
          routes, save the draft, then review and apply its deployment plan.
          Managed HTTPS needs a certificate contact and public ports 80/443. A
          resolving name alone does not prove ownership.
        </p>
        <Form
          label={`Check domain for ${server.name}`}
          action={action}
          onSubmit={(d) =>
            action.run(async () =>
              setCheck(
                await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/servers/{serverId}/check-domain",
                    {
                      params: { path },
                      body: { hostname: text(d, "hostname") },
                    },
                  ),
                ),
              ),
            )
          }
        >
          <label>
            Hostname to check
            <input
              name="hostname"
              required
              autoComplete="off"
              placeholder="app.example.com"
            />
          </label>
          <BusyButton busy={action.isPending}>Check DNS</BusyButton>
        </Form>
        {check && (
          <div role="status">
            <p>
              DNS: {check.state} ·{" "}
              {check.addresses.join(", ") || "No addresses observed"}
            </p>
            <p>{check.note}</p>
          </div>
        )}
      </div>
    </details>
  );
}

function DomainAddressForm({
  workspaceId,
  server,
  profile,
}: {
  workspaceId: string;
  server: Schema["Server"];
  profile: Schema["ServerDomain"];
}) {
  const action = useAction();
  const queries = useQueryClient();
  const [draft, setDraft] = useState({
    baseGeneration: profile.generation,
    ipv4: profile.ipv4,
    ipv6: profile.ipv6,
  });
  return (
    <Form
      label={`Domain addresses for ${server.name}`}
      action={action}
      onSubmit={() =>
        action.run(async () => {
          try {
            const value = await result(
              client.PUT(
                "/api/v1/workspaces/{workspaceId}/servers/{serverId}/domain",
                {
                  params: { path: { workspaceId, serverId: server.id } },
                  body: draft,
                },
              ),
            );
            setDraft({
              baseGeneration: value.generation,
              ipv4: value.ipv4,
              ipv6: value.ipv6,
            });
          } finally {
            await queries.invalidateQueries({
              queryKey: ["server-domain", workspaceId, server.id],
            });
          }
        })
      }
    >
      <ErrorNotice error={action.error} />
      <label>
        Public IPv4
        <input
          name="ipv4"
          disabled={action.isPending}
          value={draft.ipv4}
          onChange={(e) => setDraft({ ...draft, ipv4: e.target.value })}
          autoComplete="off"
        />
      </label>
      <label>
        Public IPv6
        <input
          name="ipv6"
          disabled={action.isPending}
          value={draft.ipv6}
          onChange={(e) => setDraft({ ...draft, ipv6: e.target.value })}
          autoComplete="off"
        />
      </label>
      <p>
        Provide at least one public address you control. Changing it changes
        every generated app name’s DNS target for this server after propagation.
        Existing routes and workloads remain in place.
      </p>
      {profile.generation !== draft.baseGeneration && (
        <div>
          <p>
            The saved configuration changed. Your entries are preserved; load
            and review the current addresses before saving again.
          </p>
          <button
            type="button"
            disabled={action.isPending}
            onClick={() =>
              setDraft({
                baseGeneration: profile.generation,
                ipv4: profile.ipv4,
                ipv6: profile.ipv6,
              })
            }
          >
            Load current addresses
          </button>
        </div>
      )}
      <BusyButton busy={action.isPending} disabled={server.transferRequired}>
        Approve domain addresses
      </BusyButton>
    </Form>
  );
}
