import { useId } from "react";
import { useTranslation } from "react-i18next";
import { backupStorageAccountPath } from "../../api/backups";
import { client, result, type Schema } from "../../api/client";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { ErrorNotice, useAction } from "../../ui";
import { ReplaceAdminKey } from "./account-forms";
import { useBackupStorageText } from "./backup-storage-text";
import * as st from "./backup-storage.styles";
import { FoundUsers } from "./found-users";
import { ManagedDestination } from "./managed-destination";

type Account = Schema["BackupStorageAccount"];

/**
 * One storage account: whose admin key it holds, what its latest check found, and the managed
 * destinations whose keys it mints, with its owners' controls for each.
 */
export function StorageAccount({
  workspaceId,
  account,
  destinations,
  servers,
  owner,
  refresh,
}: {
  workspaceId: string;
  account: Account;
  /** The managed destinations whose keys this account mints. */
  destinations: Schema["BackupDestination"][] | undefined;
  servers: Schema["Server"][] | undefined;
  owner: boolean;
  refresh: () => Promise<void>;
}) {
  const { t } = useTranslation("backup-storage");
  const text = useBackupStorageText();
  // Asking for a check needs no recent confirmation: it only reads the account.
  const check = useAction();
  const id = useId(),
    destinationsId = useId();
  const path = { workspaceId, accountId: account.id };
  return (
    <article className={s.panel} aria-labelledby={id}>
      <h3 id={id}>{account.name}</h3>
      <dl className={st.facts}>
        <dt>{t(($) => $.account.awsAccount)}</dt>
        <dd className={st.identifier}>{account.awsAccount}</dd>
        <dt>{t(($) => $.account.admin)}</dt>
        <dd className={st.identifier}>{account.adminArn}</dd>
        <dt>{t(($) => $.account.boundary)}</dt>
        <dd className={st.identifier}>{account.boundary}</dd>
        <dt>{t(($) => $.account.path)}</dt>
        <dd className={st.identifier}>{account.path}</dd>
        <dt>{t(($) => $.account.installation)}</dt>
        <dd className={st.identifier}>{account.installationId}</dd>
      </dl>
      <div className={st.part}>
        {/* Announces a check this page asked for as it waits and then reports. */}
        <p role="status">
          {account.checkedAt
            ? t(($) => $.account.checked, {
                date: text.date(account.checkedAt),
              })
            : t(($) => $.account.notChecked)}
        </p>
        {account.checkError && (
          <p className={s.notice}>
            {t(($) => $.account.checkFailed, {
              reason: text.failure(account.checkError),
            })}
          </p>
        )}
        {account.extraKeysDeleted > 0 && (
          <p className={s.notice}>
            {t(($) => $.account.extraKeys, {
              count: account.extraKeysDeleted,
            })}
          </p>
        )}
        {owner && (
          <div className={s.row}>
            <Button
              type="button"
              disabled={check.isPending}
              onClick={() =>
                check.run(async () => {
                  await result(
                    client.POST(`${backupStorageAccountPath}/check`, {
                      params: { path },
                    }),
                  );
                  await refresh();
                })
              }
            >
              {check.isPending
                ? t(($) => $.account.checking)
                : t(($) => $.account.check)}
            </Button>
          </div>
        )}
        <ErrorNotice error={check.error} />
      </div>
      <FoundUsers
        workspaceId={workspaceId}
        account={account}
        servers={servers}
        owner={owner}
        refresh={refresh}
      />
      {!!account.peers.length && (
        <div className={st.part}>
          <h4 className={st.subheading}>{t(($) => $.account.peers)}</h4>
          <p className={s.muted}>{t(($) => $.account.peersDescription)}</p>
          <ul className={st.items}>
            {account.peers.map((peer) => (
              <li key={peer} className={st.identifier}>
                {peer}
              </li>
            ))}
          </ul>
        </div>
      )}
      <section className={st.part} aria-labelledby={destinationsId}>
        <h4 id={destinationsId} className={st.subheading}>
          {t(($) => $.destinations.title)}
        </h4>
        {destinations?.length
          ? destinations.map((destination) => (
              <ManagedDestination
                key={destination.id}
                workspaceId={workspaceId}
                destination={destination}
                servers={servers}
                owner={owner}
              />
            ))
          : destinations && <p>{t(($) => $.destinations.none)}</p>}
      </section>
      {owner && <ReplaceAdminKey workspaceId={workspaceId} account={account} />}
    </article>
  );
}

/** Whether a check of the account is still to report: one asked for, or one under way. */
export function checkPending(account: Account) {
  return (
    !account.checkedAt || Date.now() - Date.parse(account.checkedAt) < 30_000
  );
}
