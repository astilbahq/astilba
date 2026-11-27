import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { backupDestinationsPath } from "../api/backups";
import { client, result, type Schema } from "../api/client";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { BusyButton, Field, Form, text, type useAction } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

// Adds a reusable backup destination. An S3 destination's keys are given to one deployment
// server, so the owner chooses it here and limits the keys to the destination's prefix or bucket:
// a compromised server then can't delete another server's backups.
export function BackupDestinationForm({
  path,
  servers,
  action,
}: {
  path: EnvironmentPath;
  servers: Schema["Server"][] | undefined;
  action: ReturnType<typeof useAction>;
}) {
  const queries = useQueryClient();
  const [kind, setKind] = useState<"filesystem" | "s3">("filesystem");
  const s3 = kind === "s3";
  return (
    <details className={s.panel}>
      <summary>Add a reusable backup destination</summary>
      <p>
        Filesystem destinations must use a root enabled by the server operator
        and share the server’s failure boundary. S3 data travels directly from
        the agent to your storage. An S3 destination belongs to one server: give
        it keys that can reach only its prefix or bucket, and add another
        destination for each other server.
      </p>
      <Form
        action={action}
        label="Add backup destination"
        onSubmit={(d, form) =>
          action.run(async () => {
            await result(
              client.POST(backupDestinationsPath, {
                params: { path },
                body: {
                  name: text(d, "name"),
                  kind,
                  root: text(d, "root"),
                  endpoint: text(d, "endpoint"),
                  bucket: text(d, "bucket"),
                  region: text(d, "region"),
                  prefix: s3 ? text(d, "prefix") || undefined : undefined,
                  serverId: s3 ? text(d, "serverId") : undefined,
                  access: {
                    accessKey: text(d, "key"),
                    secretKey: text(d, "secret"),
                    password: "",
                  },
                },
              }),
            );
            form.reset();
            setKind("filesystem");
            await queries.invalidateQueries({
              queryKey: ["backup-destinations", path.workspaceId],
            });
          })
        }
      >
        <Field name="name" label="Destination name" />
        <Label>
          Storage type
          <Select
            name="kind"
            value={kind}
            onChange={(e) => setKind(e.target.value as typeof kind)}
          >
            <option value="filesystem">Filesystem</option>
            <option value="s3">S3 compatible</option>
          </Select>
        </Label>
        {s3 ? (
          <>
            <Label>
              Server given the storage keys
              <Select name="serverId" required defaultValue="">
                <option value="">Choose a deployment server</option>
                {servers
                  ?.filter((v) => v.role !== "build" && v.status !== "revoked")
                  .map((v) => (
                    <option key={v.id} value={v.id}>
                      {v.name}
                    </option>
                  ))}
              </Select>
            </Label>
            <Field name="endpoint" label="S3 HTTPS origin" />
            <Field name="bucket" label="S3 bucket" />
            <Field name="region" label="S3 region" />
            <Field
              name="prefix"
              label="Key prefix the storage keys are limited to, such as servers/edge-1"
              required={false}
            />
            <Field
              name="key"
              label="S3 access key"
              type="password"
              autoComplete="off"
            />
            <Field
              name="secret"
              label="S3 secret key"
              type="password"
              autoComplete="off"
            />
          </>
        ) : (
          <Field name="root" label="Operator-approved filesystem root" />
        )}
        <BusyButton busy={action.isPending}>Add destination</BusyButton>
      </Form>
    </details>
  );
}
