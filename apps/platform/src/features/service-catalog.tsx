import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import * as s from "../styles";
import { ErrorNotice, Form, text, useAction } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

export function ServiceCatalog({
  path,
  spec,
  secrets,
  stage,
}: {
  path: EnvironmentPath;
  spec: Schema["Specification"];
  secrets: Schema["SecretVersion"][];
  stage: (spec: Schema["Specification"]) => void;
}) {
  const action = useAction();
  const [selected, setSelected] = useState("");
  const catalog = useQuery({
    queryKey: ["service-catalog", path.workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/service-catalog", {
          params: { path: { workspaceId: path.workspaceId } },
        }),
      ),
  });
  const recipe =
    catalog.data?.find((r) => r.id === selected) ?? catalog.data?.[0];
  return (
    <details>
      <summary>Add from service catalog</summary>
      <ErrorNotice error={catalog.error} />
      {catalog.isPending && <p role="status">Loading maintained recipes…</p>}
      {catalog.data?.length === 0 && (
        <p>No recipes are available. You can still add an image service.</p>
      )}
      {recipe && (
        <>
          <label>
            Service recipe
            <select
              value={recipe.id}
              onChange={(e) => setSelected(e.target.value)}
            >
              {catalog.data?.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name}
                </option>
              ))}
            </select>
          </label>
          <p>{recipe.description}</p>
          <ul>
            {recipe.prerequisites.map((prerequisite) => (
              <li key={prerequisite}>{prerequisite}</li>
            ))}
          </ul>
          <dl>
            {(
              ["deployment", "binding", "backup", "restore", "upgrade"] as const
            ).map((key) => (
              <div key={key}>
                <dt>
                  {key[0].toUpperCase() + key.slice(1)} ·{" "}
                  {recipe.support[key].level.replaceAll("_", " ")}
                </dt>
                <dd>{recipe.support[key].detail}</dd>
              </div>
            ))}
          </dl>
          <a href={recipe.documentation} target="_blank" rel="noreferrer">
            Upstream {recipe.name} documentation
          </a>
          <Form
            key={recipe.id}
            action={action}
            label="Stage catalog recipe"
            onSubmit={(d) => {
              const template = structuredClone(recipe.specification);
              const app = template.applications[0];
              app.id = crypto.randomUUID();
              app.name = text(d, "name");
              const volumes = new Map(
                template.volumes?.map((v) => [v.id, crypto.randomUUID()]),
              );
              template.volumes = template.volumes?.map((v) => ({
                ...v,
                id: volumes.get(v.id) as string,
                name: `${app.name} data`,
              }));
              app.mounts = app.mounts?.map((m) => ({
                ...m,
                volumeId: volumes.get(m.volumeId) as string,
              }));
              if (app.postgres)
                app.postgres.passwordSecretVersionId = text(d, "password");
              stage({
                ...spec,
                applications: [...spec.applications, app],
                volumes: [...(spec.volumes ?? []), ...(template.volumes ?? [])],
              });
            }}
          >
            <label>
              New service name
              <input
                name="name"
                defaultValue={recipe.name}
                maxLength={80}
                required
              />
            </label>
            {recipe.needsPassword && (
              <label>
                Database password secret
                <select name="password" required>
                  <option value="">Choose an existing secret version</option>
                  {secrets.map((secret) => (
                    <option key={secret.id} value={secret.id}>
                      {secret.name} · {secret.id.slice(0, 8)}
                    </option>
                  ))}
                </select>
              </label>
            )}
            {recipe.needsPassword && secrets.length === 0 && (
              <p className={s.muted}>
                Create a password in Secrets before staging this recipe.
              </p>
            )}
            <button
              type="submit"
              disabled={recipe.needsPassword && secrets.length === 0}
            >
              Stage {recipe.name} recipe
            </button>
          </Form>
        </>
      )}
    </details>
  );
}
