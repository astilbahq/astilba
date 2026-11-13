import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import { publishDraft, savedRevision } from "../api/environment";
import { serversOptions } from "../api/servers";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Anchor } from "../components/link";
import { Select } from "../components/select";
import { Textarea } from "../components/textarea";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Form, text, useAction } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

type Recipe = Schema["CatalogRecipe"];

// Staging saves a new draft revision on the server, which generates the entry's secrets into
// the secret store; their values never reach the browser.
export function ServiceCatalog({
  path,
  secrets,
  revisionId,
  dirty,
  load,
}: {
  path: EnvironmentPath;
  secrets: Schema["SecretVersion"][];
  revisionId: string;
  dirty: boolean;
  load: (revision: Schema["Revision"]) => boolean;
}) {
  const action = useAction();
  const queries = useQueryClient();
  const [selected, setSelected] = useState("");
  const [notice, setNotice] = useState("");
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
  const groups = [
    ["App templates", catalog.data?.filter((r) => r.kind === "app")],
    ["Service recipes", catalog.data?.filter((r) => r.kind === "service")],
  ] as const;
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
          <Label>
            Catalog entry
            <Select
              value={recipe.id}
              onChange={(e) => {
                setSelected(e.target.value);
                setNotice("");
              }}
            >
              {groups.map(
                ([label, entries]) =>
                  !!entries?.length && (
                    <optgroup label={label} key={label}>
                      {entries.map((r) => (
                        <option key={r.id} value={r.id}>
                          {r.version ? `${r.name} ${r.version}` : r.name}
                        </option>
                      ))}
                    </optgroup>
                  ),
              )}
            </Select>
          </Label>
          <p>{recipe.description}</p>
          {recipe.kind === "app" && (
            <p>
              Services:{" "}
              {recipe.specification.applications.map((a) => a.name).join(", ")}.
            </p>
          )}
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
          <Anchor href={recipe.documentation} target="_blank" rel="noreferrer">
            Upstream {recipe.name} documentation
          </Anchor>
          <StageForm
            key={recipe.id}
            recipe={recipe}
            path={path}
            secrets={secrets}
            dirty={dirty}
            action={action}
            onSubmit={(body) =>
              action.run(async () => {
                setNotice("");
                const revision = savedRevision(
                  await result(
                    client.POST(
                      "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/catalog-stagings",
                      {
                        params: { path },
                        body: { ...body, baseRevisionId: revisionId },
                      },
                    ),
                  ),
                );
                await publishDraft(queries, path, revision, revisionId);
                await Promise.all([
                  queries.invalidateQueries({ queryKey: ["secrets", path] }),
                  queries.invalidateQueries({ queryKey: ["revisions", path] }),
                  queries.invalidateQueries({
                    queryKey: ["catalog-stagings", path],
                  }),
                ]);
                load(revision);
                setNotice(
                  `${body.name} is in a new saved revision. Review a deployment plan to apply it.`,
                );
              })
            }
          />
          {notice && <p role="status">{notice}</p>}
        </>
      )}
    </details>
  );
}

function StageForm({
  recipe,
  path,
  secrets,
  dirty,
  action,
  onSubmit,
}: {
  recipe: Recipe;
  path: EnvironmentPath;
  secrets: Schema["SecretVersion"][];
  dirty: boolean;
  action: ReturnType<typeof useAction>;
  onSubmit: (
    body: Omit<Schema["CatalogStageRequest"], "baseRevisionId">,
  ) => void;
}) {
  const [name, setName] = useState(recipe.name);
  const servers = useQuery({
    ...serversOptions(path.workspaceId),
    enabled: !!recipe.route,
  });
  const address = useQuery({
    queryKey: ["client-address", path.workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/client-address", {
          params: { path: { workspaceId: path.workspaceId } },
        }),
      ),
    enabled: !!recipe.route,
    staleTime: Infinity,
  });
  // Each secret is named after the service it belongs to, as staging names it.
  const serviceName = (id: string) => {
    const original =
      recipe.specification.applications.find((a) => a.id === id)?.name ?? "";
    return original.startsWith(recipe.name)
      ? name + original.slice(recipe.name.length)
      : original;
  };
  return (
    <Form
      action={action}
      label={`Stage ${recipe.name}`}
      onSubmit={(d) => {
        const existingSecrets = recipe.secrets
          .map((secret) => ({
            secretId: secret.id,
            secretVersionId: text(d, `secret-${secret.id}`),
          }))
          .filter((v) => v.secretVersionId);
        const domain = text(d, "domain").trim();
        const server = text(d, "server");
        onSubmit({
          recipeId: recipe.id,
          name: text(d, "name"),
          existingSecrets,
          route: recipe.route
            ? {
                ...(domain ? { domain } : { generatedServerId: server }),
                tls: text(d, "tls") as "managed" | "off",
                allowCidrs: text(d, "allow")
                  .split(/[\s,]+/)
                  .filter(Boolean),
              }
            : undefined,
        });
      }}
    >
      <Label>
        {recipe.kind === "app" ? "Name" : "New service name"}
        <Input
          name="name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={60}
          required
        />
      </Label>
      {recipe.secrets.map((secret) => (
        <Label key={secret.id}>
          {serviceName(secret.serviceId)} · {secret.name}
          <Select name={`secret-${secret.id}`} defaultValue="">
            <option value="">Generate a new value</option>
            {secrets.map((v) => (
              <option key={v.id} value={v.id}>
                Use {v.name} · {v.id.slice(0, 8)}
              </option>
            ))}
          </Select>
        </Label>
      ))}
      {recipe.route && (
        <fieldset className={s.stack}>
          <legend>Public route</legend>
          <Label>
            Domain
            <Input
              name="domain"
              placeholder="tracker.example.com"
              maxLength={253}
              autoCapitalize="none"
              spellCheck={false}
            />
          </Label>
          <Label>
            Or a generated application domain on
            <Select name="server" defaultValue="">
              <option value="">No generated domain</option>
              {servers.data
                ?.filter(
                  (v) =>
                    v.role !== "build" &&
                    v.status !== "revoked" &&
                    !v.transferRequired,
                )
                .map((v) => (
                  <option value={v.id} key={v.id}>
                    {v.name}
                  </option>
                ))}
            </Select>
          </Label>
          <Label>
            TLS
            <Select name="tls" defaultValue="managed">
              <option value="managed">Managed certificate (HTTPS)</option>
              <option value="off">Off (HTTP)</option>
            </Select>
          </Label>
          <Label>
            Allowed client networks
            <Textarea
              key={address.data?.prefix}
              name="allow"
              rows={2}
              required
              defaultValue={address.data?.prefix ?? ""}
              spellCheck={false}
            />
          </Label>
          <p className={s.muted}>
            Only these networks can reach {name} until you change the route.
            Your network is filled in as management sees it; a server elsewhere
            may see you differently. {recipe.name} makes the first account its
            administrator, so create yours before opening the route.
          </p>
        </fieldset>
      )}
      {dirty && (
        <p className={s.muted}>
          Save or discard your changes first: staging saves a new revision.
        </p>
      )}
      <BusyButton busy={action.isPending} disabled={dirty}>
        Stage {recipe.name}
      </BusyButton>
    </Form>
  );
}
