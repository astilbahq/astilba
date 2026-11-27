import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
  backupDestinationsOptions,
  backupStorageAccountsOptions,
} from "../../api/backups";
import { serversOptions } from "../../api/servers";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { ErrorNotice } from "../../ui";
import { AddStorageAccount } from "./account-forms";
import * as st from "./backup-storage.styles";
import { checkPending, StorageAccount } from "./storage-account";

/** The heading alerts and the destination form link to. */
export const backupStorageSection = "backup-storage-accounts";

/**
 * The workspace's AWS storage accounts, which management mints managed destinations' per-server
 * keys with, and each destination's keys (docs/backups.md#managed-keys). Every member sees them,
 * since alerts link here; owners change them.
 */
export function BackupStorage({
  workspaceId,
  owner,
}: {
  workspaceId: string;
  owner: boolean;
}) {
  const { t } = useTranslation("backup-storage");
  const queries = useQueryClient();
  const accounts = useQuery({
    ...backupStorageAccountsOptions(workspaceId),
    // An account check runs shortly after it is asked for and records what it found once done.
    refetchInterval: (query) =>
      query.state.data?.some(checkPending) ? 5000 : false,
  });
  const managed = !!accounts.data?.length;
  const destinations = useQuery({
    ...backupDestinationsOptions(workspaceId),
    enabled: managed,
    // Keys are made and revoked by management, in the background.
    refetchInterval: (query) =>
      query.state.data?.some((d) =>
        d.keys.some((k) => k.state === "minting" || k.state === "revoking"),
      )
        ? 5000
        : false,
  });
  const servers = useQuery({
    ...serversOptions(workspaceId),
    enabled: managed,
  });
  const refresh = () =>
    queries.invalidateQueries({
      queryKey: backupStorageAccountsOptions(workspaceId).queryKey,
    });
  return (
    <section className={s.panel} aria-labelledby={backupStorageSection}>
      <h2 id={backupStorageSection} tabIndex={-1} className={st.focusTarget}>
        {t(($) => $.title)}
      </h2>
      <p className={s.muted}>{t(($) => $.description)}</p>
      {accounts.isPending ? (
        <p role="status">{t(($) => $.loading)}</p>
      ) : !accounts.data ? (
        <div className={s.stack}>
          <ErrorNotice
            error={accounts.error}
            message={t(($) => $.loadFailed)}
          />
          <div className={s.row}>
            <Button
              type="button"
              disabled={accounts.isFetching}
              onClick={() => void accounts.refetch()}
            >
              {t(($) => $.retry)}
            </Button>
          </div>
        </div>
      ) : !accounts.data.length ? (
        <p>{t(($) => $.empty)}</p>
      ) : (
        <>
          <ErrorNotice error={destinations.error ?? servers.error} />
          {accounts.data.map((account) => (
            <StorageAccount
              key={account.id}
              workspaceId={workspaceId}
              account={account}
              destinations={destinations.data?.filter(
                (d) => d.accountId === account.id,
              )}
              servers={servers.data}
              owner={owner}
              refresh={refresh}
            />
          ))}
        </>
      )}
      {owner && <AddStorageAccount workspaceId={workspaceId} />}
    </section>
  );
}
