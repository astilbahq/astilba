import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  backupDestinationsOptions,
  backupStorageAccountPath,
  backupStorageAccountsOptions,
  backupStorageAccountsPath,
} from "../../api/backups";
import { ApiError, client, result, type Schema } from "../../api/client";
import { Anchor } from "../../components/link";
import { Radio } from "../../components/radio";
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

// The period new accounts start with, as migration 00079 gave existing ones.
const suggestedRotationDays = 90;

/**
 * How old the account's destinations' keys may grow before management replaces them. Turning
 * that off is a choice of its own rather than an empty field. Resetting the form chooses `days`
 * again; nothing else remounts the fields, so focus stays where the owner left it.
 */
function KeyRotationFields({ days }: { days: number | null }) {
  const { t } = useTranslation("backup-storage");
  const [on, setOn] = useState(days !== null);
  const onId = useId(),
    offId = useId();
  const fields = useRef<HTMLFieldSetElement>(null);
  useEffect(() => {
    const form = fields.current?.form;
    const reset = () => setOn(days !== null);
    form?.addEventListener("reset", reset);
    return () => form?.removeEventListener("reset", reset);
  }, [days]);
  return (
    <fieldset ref={fields} className={s.choices}>
      <legend className={s.choiceLegend}>{t(($) => $.rotation.legend)}</legend>
      <p className={s.muted}>{t(($) => $.rotation.description)}</p>
      <label htmlFor={onId} className={s.choice}>
        <Radio
          id={onId}
          name="rotation"
          value="on"
          checked={on}
          onChange={() => setOn(true)}
        />
        <span>{t(($) => $.rotation.on)}</span>
      </label>
      {on && (
        <Field
          name="keyRotationDays"
          label={t(($) => $.rotation.days)}
          type="number"
          inputMode="numeric"
          min={30}
          max={365}
          defaultValue={days ?? suggestedRotationDays}
        />
      )}
      <label htmlFor={offId} className={s.choice}>
        <Radio
          id={offId}
          name="rotation"
          value="off"
          checked={!on}
          onChange={() => setOn(false)}
        />
        <span className={s.choiceText}>
          <span>{t(($) => $.rotation.off)}</span>
          <span className={s.muted}>{t(($) => $.rotation.offDescription)}</span>
        </span>
      </label>
    </fieldset>
  );
}

const keyRotationDays = (d: FormData) =>
  text(d, "rotation") === "off" ? null : Number(text(d, "keyRotationDays"));

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
                  keyRotationDays: keyRotationDays(d),
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
        <KeyRotationFields days={suggestedRotationDays} />
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

/**
 * Changes how old the account's destinations' keys may grow before management replaces them, or
 * turns that off. Each change is audited.
 */
export function KeyRotation({
  workspaceId,
  account,
}: {
  workspaceId: string;
  account: Schema["BackupStorageAccount"];
}) {
  const { t } = useTranslation("backup-storage");
  const action = useAction({ recentAuth: true });
  const queries = useQueryClient();
  const [saved, setSaved] = useState(false);
  return (
    <details>
      <summary>{t(($) => $.rotation.summary)}</summary>
      <Form
        action={action}
        label={t(($) => $.rotation.label, { name: account.name })}
        onSubmit={(d) =>
          action.run(async () => {
            setSaved(false);
            await result(
              client.PUT(`${backupStorageAccountPath}/key-rotation`, {
                params: { path: { workspaceId, accountId: account.id } },
                body: { keyRotationDays: keyRotationDays(d) },
              }),
            );
            setSaved(true);
            // The destinations show when their keys are replaced, and a shorter period can start
            // a replacement at once.
            await Promise.all(
              [
                backupStorageAccountsOptions(workspaceId),
                backupDestinationsOptions(workspaceId),
              ].map(({ queryKey }) => queries.invalidateQueries({ queryKey })),
            );
          })
        }
      >
        <ErrorNotice error={action.error} />
        <KeyRotationFields days={account.keyRotationDays} />
        <BusyButton busy={action.isPending}>
          {t(($) => $.rotation.submit)}
        </BusyButton>
      </Form>
      <p role="status">{saved ? t(($) => $.rotation.saved) : null}</p>
    </details>
  );
}
