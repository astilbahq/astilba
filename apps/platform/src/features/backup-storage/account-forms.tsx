import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  backupStorageAccountPath,
  backupStorageAccountsOptions,
  backupStorageAccountsPath,
} from "../../api/backups";
import { ApiError, client, result, type Schema } from "../../api/client";
import { Anchor } from "../../components/link";
import * as s from "../../styles";
import {
  BusyButton,
  ErrorNotice,
  Field,
  Form,
  text,
  useAction,
} from "../../ui";
import * as st from "./backup-storage.styles";

export const managedKeysGuide =
  "https://github.com/astilbahq/astilba/blob/main/docs/backups.md#managed-keys";

// The admin credential's key. The server names a refused key `credential`.
function KeyFields() {
  const { t } = useTranslation("backup-storage");
  return (
    <>
      <Field
        name="accessKeyId"
        label={t(($) => $.credential.accessKeyId)}
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
      />
      <Field
        name="secretAccessKey"
        label={t(($) => $.credential.secretAccessKey)}
        type="password"
        autoComplete="off"
      />
    </>
  );
}

const adminKey = (d: FormData) => ({
  accessKeyId: text(d, "accessKeyId"),
  secretAccessKey: text(d, "secretAccessKey"),
});

/** Adds a storage account: an AWS admin credential management checks before storing it. */
export function AddStorageAccount({ workspaceId }: { workspaceId: string }) {
  const { t } = useTranslation("backup-storage");
  // The admin credential is sealed like any storage key, so adding it needs a recent confirmation.
  const action = useAction({ recentAuth: true });
  const queries = useQueryClient();
  const [added, setAdded] = useState("");
  return (
    <details className={s.panel}>
      <summary>{t(($) => $.add.summary)}</summary>
      <p>
        {t(($) => $.add.instructions, {
          path: `/astilba/${workspaceId}/`,
        })}
      </p>
      <p>
        <Anchor href={managedKeysGuide} target="_blank" rel="noreferrer">
          {t(($) => $.guide)}
        </Anchor>
      </p>
      <Form
        action={action}
        label={t(($) => $.add.label)}
        onSubmit={(d, form) =>
          action.run(async () => {
            setAdded("");
            const account = await result(
              client.POST(backupStorageAccountsPath, {
                params: { path: { workspaceId } },
                body: {
                  name: text(d, "name"),
                  ...adminKey(d),
                  boundary: text(d, "boundary").trim(),
                },
              }),
            );
            form.reset();
            setAdded(t(($) => $.add.added, { name: account.name }));
            await queries.invalidateQueries({
              queryKey: backupStorageAccountsOptions(workspaceId).queryKey,
            });
          })
        }
      >
        <ErrorNotice error={action.error} />
        <Field name="name" label={t(($) => $.add.name)} />
        <KeyFields />
        {/* The server names a boundary from another account `boundary`. */}
        <Field
          name="boundary"
          label={t(($) => $.credential.boundary)}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
        />
        <BusyButton busy={action.isPending}>
          {t(($) => $.add.submit)}
        </BusyButton>
      </Form>
      <p role="status">{added}</p>
    </details>
  );
}

/**
 * Replaces a storage account's admin key with another of the same AWS account. The account keeps
 * its permissions boundary, which every user minted under it carries. The generation it replaces
 * keeps two replacements from interleaving; a refusal reloads the account, so the next attempt
 * replaces the current one.
 */
export function ReplaceAdminKey({
  workspaceId,
  account,
}: {
  workspaceId: string;
  account: Schema["BackupStorageAccount"];
}) {
  const { t } = useTranslation("backup-storage");
  const action = useAction({ recentAuth: true });
  const queries = useQueryClient();
  const [replaced, setReplaced] = useState(false);
  const refresh = () =>
    queries.invalidateQueries({
      queryKey: backupStorageAccountsOptions(workspaceId).queryKey,
    });
  return (
    <details>
      <summary>{t(($) => $.replace.summary)}</summary>
      <div className={s.stack}>
        <p>{t(($) => $.replace.instructions)}</p>
        <dl className={st.facts}>
          <dt>{t(($) => $.account.boundary)}</dt>
          <dd className={st.identifier}>{account.boundary}</dd>
        </dl>
        <p className={s.muted}>{t(($) => $.replace.boundaryStays)}</p>
        <Form
          action={action}
          label={t(($) => $.replace.label, { name: account.name })}
          onSubmit={(d, form) =>
            action.run(async () => {
              setReplaced(false);
              await result(
                client.PUT(`${backupStorageAccountPath}/credential`, {
                  params: { path: { workspaceId, accountId: account.id } },
                  body: { ...adminKey(d), generation: account.generation },
                }),
              ).catch(async (error: unknown) => {
                if (error instanceof ApiError && error.status === 409)
                  await refresh();
                throw error;
              });
              form.reset();
              setReplaced(true);
              await refresh();
            })
          }
        >
          <ErrorNotice error={action.error} />
          <KeyFields />
          <BusyButton busy={action.isPending}>
            {t(($) => $.replace.submit)}
          </BusyButton>
        </Form>
        <p role="status">{replaced ? t(($) => $.replace.replaced) : null}</p>
      </div>
    </details>
  );
}
