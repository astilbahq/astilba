import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import * as s from "../styles";
import { ErrorNotice, Form, text, useAction } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

const capabilities: {
  value: Schema["AutomationCapability"];
  label: string;
}[] = [
  { value: "environment:read", label: "Read configuration and runtime status" },
  { value: "configuration:write", label: "Save configuration drafts" },
  { value: "deployment:plan", label: "Prepare deployment plans" },
  { value: "deployment:apply", label: "Apply reviewed deployment plans" },
  { value: "build:run", label: "Re-run owner-configured build targets" },
];

export function AutomationCredentials({ path }: { path: EnvironmentPath }) {
  const action = useAction();
  const [revealed, setRevealed] = useState<{ id: string; token: string }>();
  const [message, setMessage] = useState("");
  const credentials = useQuery({
    queryKey: ["automation-credentials", path],
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/automation-credentials",
          { params: { path } },
        ),
      ),
  });
  return (
    <section className={s.panel} aria-label="Environment automation">
      <h2>Automation</h2>
      <p>
        Give CI, the CLI or an MCP client selected access to this environment.
        Tokens expire and can be revoked. They cannot manage accounts, secrets,
        host permissions or recovery.
      </p>
      <p className={s.muted}>
        Creating or revoking a token requires recent identity confirmation.{" "}
        <a href="/account" target="_blank" rel="noreferrer">
          Open Account security in a new tab
        </a>{" "}
        to confirm, then return here and retry.
      </p>
      <ErrorNotice error={action.error ?? credentials.error} />
      {message && <p role="status">{message}</p>}
      {revealed && (
        <section className={s.notice} aria-label="New automation token">
          <p role="status">
            Save this token privately now. It is shown once and disappears when
            you leave this environment.
          </p>
          <label>
            Automation token
            <textarea
              className={s.code}
              readOnly
              rows={3}
              value={revealed.token}
              spellCheck={false}
              autoComplete="off"
              onFocus={(event) => event.currentTarget.select()}
            />
          </label>
          <button type="button" onClick={() => setRevealed(undefined)}>
            I have saved the token
          </button>
        </section>
      )}
      <details>
        <summary>Create automation token</summary>
        <Form
          action={action}
          label="Create automation credential"
          onSubmit={(data, form) =>
            action.run(async () => {
              const selected = data.getAll(
                "capability",
              ) as Schema["AutomationCapability"][];
              if (selected.length === 0)
                throw new Error("Choose at least one capability.");
              const created = await result(
                client.POST(
                  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/automation-credentials",
                  {
                    params: { path },
                    body: {
                      name: text(data, "name"),
                      capabilities: selected,
                      expiresAt: new Date(
                        Date.now() + Number(text(data, "days")) * 86400000,
                      ).toISOString(),
                    },
                  },
                ),
              );
              if (created.token)
                setRevealed({
                  id: created.credential.id,
                  token: created.token,
                });
              setMessage("Automation credential created for this environment.");
              form.reset();
              await credentials.refetch();
            })
          }
        >
          <label>
            Token name
            <input name="name" required maxLength={80} />
          </label>
          <label>
            Expires after
            <select name="days" defaultValue="7">
              <option value="1">1 day</option>
              <option value="7">7 days</option>
              <option value="30">30 days</option>
              <option value="90">90 days</option>
            </select>
          </label>
          <fieldset className={s.stack}>
            <legend>Capabilities</legend>
            {capabilities.map(({ value, label }) => (
              <label key={value} className={s.row}>
                <input
                  name="capability"
                  type="checkbox"
                  value={value}
                  defaultChecked={value === "environment:read"}
                />
                {label}
              </label>
            ))}
          </fieldset>
          <button type="submit" disabled={action.isPending || !!revealed}>
            {action.isPending ? "Working…" : "Create token"}
          </button>
        </Form>
      </details>
      {credentials.isPending && <p role="status">Loading credentials…</p>}
      {credentials.data?.length === 0 && <p>No automation credentials yet.</p>}
      <ul className={s.list}>
        {credentials.data?.map((credential) => {
          const active =
            !credential.revokedAt &&
            Date.parse(credential.expiresAt) > Date.now();
          return (
            <li key={credential.id}>
              <strong>{credential.name}</strong>
              <p className={s.muted}>
                {credential.revokedAt
                  ? "Revoked"
                  : active
                    ? "Active"
                    : "Expired"}
                {" · "}Expires{" "}
                <time dateTime={credential.expiresAt}>
                  {new Date(credential.expiresAt).toLocaleString()}
                </time>
              </p>
              <p>{credential.capabilities.join(", ")}</p>
              {active && (
                <button
                  type="button"
                  disabled={action.isPending}
                  onClick={() => {
                    if (
                      window.confirm(
                        `Revoke ${credential.name}? New requests will be blocked. Already accepted operations keep their existing completion rules.`,
                      )
                    )
                      action.run(async () => {
                        await result(
                          client.DELETE(
                            "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/automation-credentials/{credentialId}",
                            {
                              params: {
                                path: { ...path, credentialId: credential.id },
                              },
                            },
                          ),
                        );
                        if (revealed?.id === credential.id)
                          setRevealed(undefined);
                        setMessage(`${credential.name} revoked.`);
                        await credentials.refetch();
                      });
                  }}
                >
                  Revoke {credential.name}
                </button>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
