import type { UseQueryResult } from "@tanstack/react-query";
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
  /** The workspace's destinations; this account mints the keys of those naming it. */
  destinations: UseQueryResult<Schema["BackupDestination"][]>;
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
  const managed = destinations.data?.filter((d) => d.accountId === account.id);
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
        {/* Announces a check as it starts and once it reports. */}
        <p role="status">
          {account.checking && <>{t(($) => $.account.checking)} </>}
          {account.checkedAt
            ? t(($) => $.account.checked, {
                date: text.date(account.checkedAt),
              })
            : !account.checking && t(($) => $.account.notChecked)}
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
              // Keeps focus while busy, so a refusal is read beside it.
              focusableWhenDisabled
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
        {!managed ? (
          destinations.isPending ? (
            <p role="status">{t(($) => $.destinations.loading)}</p>
          ) : (
            <div className={s.stack}>
              <ErrorNotice
                error={destinations.error}
                message={t(($) => $.destinations.loadFailed)}
              />
              <div className={s.row}>
                <Button
                  type="button"
                  focusableWhenDisabled
                  disabled={destinations.isFetching}
                  onClick={() => void destinations.refetch()}
                >
                  {t(($) => $.retry)}
                </Button>
              </div>
            </div>
          )
        ) : managed.length ? (
          managed.map((destination) => (
            <ManagedDestination
              key={destination.id}
              workspaceId={workspaceId}
              destination={destination}
              servers={servers}
              owner={owner}
            />
          ))
        ) : (
          <p>{t(($) => $.destinations.none)}</p>
        )}
      </section>
      {owner && <ReplaceAdminKey workspaceId={workspaceId} account={account} />}
    </article>
  );
}
