import { useQuery } from "@tanstack/react-query";
import type { RefObject } from "react";
import { client, result, type Schema } from "../api/client";
import { Button } from "../components/button";
import * as s from "../styles";
import type { EnvironmentPath } from "./visual-editor";

export type RecommendedBackup = Schema["CatalogBackupPreset"] & {
  app: Schema["ApplicationReference"];
  from: string;
};

// The backups the catalog recommends for services it staged that are still in the
// configuration and have no policy yet. Choosing one fills in the policy form; the owner
// still chooses the destination and password.
export function BackupRecommendations({
  path,
  revision,
  policies,
  form,
  choose,
}: {
  path: EnvironmentPath;
  revision: Schema["Revision"];
  policies: Schema["BackupPolicy"][] | undefined;
  // The create-policy disclosure. Choosing a recommendation opens it and moves to its name
  // field. The form refills only the fields a recommendation sets (each is keyed by it), so
  // a destination or password already entered stays.
  form: RefObject<HTMLDetailsElement | null>;
  choose: (backup: RecommendedBackup) => void;
}) {
  const stagings = useQuery({
    queryKey: ["catalog-stagings", path],
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/catalog-stagings",
          { params: { path } },
        ),
      ),
  });
  const recommended = (stagings.data ?? []).flatMap((staging) =>
    staging.backups.flatMap((preset): RecommendedBackup[] => {
      const app = revision.specification.applications.find(
        (a) => a.id === preset.serviceId,
      );
      return app &&
        policies &&
        !policies.some((p) => p.resourceId === preset.serviceId)
        ? [
            {
              ...preset,
              app,
              from: `${staging.recipeName} ${staging.recipeVersion}`,
            },
          ]
        : [];
    }),
  );
  if (recommended.length === 0) return null;
  return (
    <div className={s.panel} role="group" aria-label="Recommended backups">
      <h3>Recommended backups</h3>
      <ul className={s.stack}>
        {recommended.map((r) => (
          <li key={r.serviceId}>
            {r.app.name}: daily at {String(r.hour).padStart(2, "0")}:
            {String(r.minute).padStart(2, "0")}, keeping {r.retain} uploads, as
            the {r.from} template recommends.{" "}
            <Button
              onClick={() => {
                choose(r);
                if (form.current) form.current.open = true;
                // After the form renders with the recommended values.
                requestAnimationFrame(() =>
                  form.current
                    ?.querySelector<HTMLInputElement>('input[name="name"]')
                    ?.focus(),
                );
              }}
            >
              Use for {r.app.name}
            </Button>
          </li>
        ))}
      </ul>
      <p>Choose a destination and a repository password to finish.</p>
    </div>
  );
}
