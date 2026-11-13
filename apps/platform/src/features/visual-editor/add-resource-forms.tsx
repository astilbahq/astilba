import { useId, useState } from "react";
import type { Schema } from "../../api/client";
import { Button } from "../../components/button";
import { Input } from "../../components/input";
import { Label } from "../../components/label";
import { Select } from "../../components/select";
import * as s from "../../styles";
import { Field, Form, text, type useAction } from "../../ui";
import { currentReleases, type PostgresRelease } from "../postgres-releases";

type Spec = Schema["Specification"];
type App = Schema["ApplicationReference"];

export function VolumeNameField({ name }: { name: string }) {
  const id = useId();
  const [input, setInput] = useState({ name, value: name });
  if (input.name !== name) setInput({ name, value: name });
  return (
    <Label htmlFor={id}>
      Volume name
      <Input
        id={id}
        name="name"
        value={input.value}
        onChange={(event) => setInput({ name, value: event.target.value })}
        required
      />
    </Label>
  );
}

export function AddServiceForm({
  spec,
  secrets,
  postgresReleases,
  action,
  stage,
  onSelect,
}: {
  spec: Spec;
  secrets: Schema["SecretVersion"][];
  postgresReleases: PostgresRelease[];
  action: ReturnType<typeof useAction>;
  stage: (spec: Spec) => void;
  onSelect: (id: string) => void;
}) {
  const offered = currentReleases(postgresReleases);
  return (
    <details className={s.workspaceUtility}>
      <summary>Add a service</summary>
      <Form
        action={action}
        label="Add service"
        onSubmit={(d, form) => {
          const kind = text(d, "kind") as "web" | "worker" | "postgres";
          const release = offered.find(
            (r) => r.image === text(d, "postgresImage"),
          );
          if (kind === "postgres" && !release) return;
          const id = crypto.randomUUID();
          let next: Spec = {
            ...spec,
            applications: [...spec.applications],
          };
          const service: App = {
            id,
            name: text(d, "name"),
            kind,
            source: {
              kind: "image",
              image:
                kind === "postgres" && release
                  ? release.image
                  : text(d, "image"),
            },
          };
          if (kind === "postgres" && release) {
            const volumeId = crypto.randomUUID();
            service.postgres = {
              database: "app",
              username: "app",
              passwordSecretVersionId: text(d, "password"),
            };
            service.mounts = [
              {
                volumeId,
                target: release.mount,
                readOnly: false,
              },
            ];
            next = {
              ...next,
              volumes: [
                ...(next.volumes ?? []),
                { id: volumeId, name: `${service.name} data` },
              ],
            };
          }
          next.applications.push(service);
          stage(next);
          onSelect(id);
          form.reset();
        }}
      >
        <Field name="name" label="Service name" />
        <Label>
          Service kind
          <Select name="kind">
            <option value="web">Web application</option>
            <option value="worker">Worker</option>
            <option value="postgres">PostgreSQL</option>
          </Select>
        </Label>
        <Field
          name="image"
          label="Container image (web or worker)"
          required={false}
        />
        <Label>
          Database password secret (PostgreSQL)
          <Select name="password">
            <option value="">Select a secret version</option>
            {secrets.map((secret) => (
              <option key={secret.id} value={secret.id}>
                {secret.name} · {secret.id.slice(0, 8)}
              </option>
            ))}
          </Select>
        </Label>
        <Label>
          PostgreSQL version for a new database
          <Select
            name="postgresImage"
            defaultValue={offered[0]?.image}
            key={offered[0]?.image}
          >
            {offered.map((release) => (
              <option key={release.image} value={release.image}>
                {release.name}
              </option>
            ))}
          </Select>
        </Label>
        {offered.length === 0 && (
          <small role="status">Loading PostgreSQL versions…</small>
        )}

        <small>
          Create the password in Secrets below before adding PostgreSQL. Its
          data volume is created automatically.
        </small>
        <Button type="submit">Stage new service</Button>
      </Form>
    </details>
  );
}

export function AddVolumeForm({
  spec,
  action,
  stage,
}: {
  spec: Spec;
  action: ReturnType<typeof useAction>;
  stage: (spec: Spec) => void;
}) {
  return (
    <details className={s.workspaceUtility}>
      <summary>Add a data volume</summary>
      <Form
        action={action}
        onSubmit={(d, form) => {
          stage({
            ...spec,
            volumes: [
              ...(spec.volumes ?? []),
              { id: crypto.randomUUID(), name: text(d, "name") },
            ],
          });
          form.reset();
        }}
      >
        <Field name="name" label="Volume name" />
        <Button type="submit">Stage volume</Button>
      </Form>
    </details>
  );
}
