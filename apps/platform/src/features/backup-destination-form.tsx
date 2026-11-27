import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import {
  backupDestinationsOptions,
  backupDestinationsPath,
  backupStorageAccountsOptions,
} from "../api/backups";
import { client, result, type Schema } from "../api/client";
import { Label } from "../components/label";
import { Link } from "../components/link";
import { Select } from "../components/select";
import * as s from "../styles";
import {
  BusyButton,
  ErrorNotice,
  Field,
  Form,
  text,
  type useAction,
  useFieldError,
} from "../ui";
import { backupStorageSection } from "./backup-storage/backup-storage";
import type { EnvironmentPath } from "./visual-editor";

// Adds a reusable backup destination. An S3 destination's keys are given to one deployment
// server, so the owner chooses it here and limits the keys to the destination's prefix or bucket:
// a compromised server then can't delete another server's backups. On AWS, a managed destination
// has management mint that server's key from a storage account instead, limited for it, and
// revoke it with the server (docs/backups.md#managed-keys).
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
  const accounts = useQuery(backupStorageAccountsOptions(path.workspaceId));
  const [kind, setKind] = useState<"filesystem" | "s3" | "managed">(
    "filesystem",
  );
  // A managed destination is an S3 one whose key management makes.
  const managed = kind === "managed" && !!accounts.data?.length;
  const s3 = kind === "s3" || managed;
  return (
    <details className={s.panel}>
      <summary>Add a reusable backup destination</summary>
      <p>
        Filesystem destinations must use a root enabled by the server operator
        and share the server’s failure boundary. S3 data travels directly from
        the agent to your storage. An S3 destination belongs to one server: give
        it keys that can reach only its prefix or bucket, and add another
        destination for each other server. On AWS, management can make and
        revoke each server’s keys itself, from a{" "}
        <Link
          to="/members"
          search={{ workspace: path.workspaceId }}
          hash={backupStorageSection}
        >
          backup storage account
        </Link>
        .
      </p>
      <ErrorNotice error={accounts.error} />
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
                  kind: s3 ? "s3" : "filesystem",
                  root: text(d, "root"),
                  // Management derives a managed destination's endpoint from its region.
                  endpoint: text(d, "endpoint"),
                  bucket: text(d, "bucket"),
                  region: text(d, "region"),
                  prefix: s3 ? text(d, "prefix") || undefined : undefined,
                  serverId: s3 ? text(d, "serverId") : undefined,
                  ...(managed
                    ? { accountId: text(d, "accountId") }
                    : {
                        access: {
                          accessKey: text(d, "key"),
                          secretKey: text(d, "secret"),
                          password: "",
                        },
                      }),
                },
              }),
            );
            form.reset();
            setKind("filesystem");
            await queries.invalidateQueries({
              queryKey: backupDestinationsOptions(path.workspaceId).queryKey,
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
            <option value="s3">S3 compatible, with keys you make</option>
            {!!accounts.data?.length && (
              <option value="managed">
                AWS S3, with keys management makes
              </option>
            )}
          </Select>
        </Label>
        {s3 ? (
          <>
            {managed && (
              <>
                <p>
                  Management makes the server’s key from the storage account,
                  limited to this destination’s repositories, and revokes it
                  when the server is revoked. Adding the destination waits until
                  AWS has the key working, which takes a few seconds.
                </p>
                <AccountSelect accounts={accounts.data} />
              </>
            )}
            <ServerSelect servers={servers} />
            {!managed && <Field name="endpoint" label="S3 HTTPS origin" />}
            <Field name="bucket" label="S3 bucket" />
            <Field
              name="region"
              label={
                managed
                  ? "AWS region, such as eu-west-2 (China and GovCloud regions aren't supported)"
                  : "S3 region"
              }
            />
            <Field
              name="prefix"
              label="Key prefix the storage keys are limited to, such as servers/edge-1"
              required={false}
            />
            {!managed && (
              <>
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
            )}
          </>
        ) : (
          <Field name="root" label="Operator-approved filesystem root" />
        )}
        <BusyButton busy={action.isPending}>Add destination</BusyButton>
      </Form>
    </details>
  );
}

// The storage accounts a managed destination's key can be minted from.
function AccountSelect({
  accounts,
}: {
  accounts: Schema["BackupStorageAccount"][] | undefined;
}) {
  const error = useFieldError("accountId");
  return (
    <Label>
      Storage account the key is made from
      <Select
        name="accountId"
        required
        defaultValue={accounts?.length === 1 ? accounts[0].id : ""}
        {...error}
      >
        <option value="">Choose a storage account</option>
        {accounts?.map((a) => (
          <option key={a.id} value={a.id}>
            {a.name} ({a.awsAccount})
          </option>
        ))}
      </Select>
    </Label>
  );
}

// The deployment servers a destination can be given to: enrolled ones whose agent can deploy.
function ServerSelect({
  servers,
}: {
  servers: Schema["Server"][] | undefined;
}) {
  const error = useFieldError("serverId");
  return (
    <Label>
      Server given the storage keys
      <Select name="serverId" required defaultValue="" {...error}>
        <option value="">Choose a deployment server</option>
        {servers
          ?.filter(
            (v) =>
              v.role !== "build" &&
              v.status !== "revoked" &&
              v.status !== "pending" &&
              !v.transferRequired &&
              v.capabilities?.docker &&
              v.preflight?.ready,
          )
          .map((v) => (
            <option key={v.id} value={v.id}>
              {v.name}
            </option>
          ))}
      </Select>
    </Label>
  );
}
