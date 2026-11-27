import { useId, useRef } from "react";
import { useTranslation } from "react-i18next";
import { backupStorageAccountPath } from "../../api/backups";
import { client, result, type Schema } from "../../api/client";
import * as s from "../../styles";
import { useBackupStorageText } from "./backup-storage-text";
import * as st from "./backup-storage.styles";
import { ConfirmAction } from "./confirm-action";

type Account = Schema["BackupStorageAccount"];
type FoundUser = Schema["BackupStorageFoundUser"];

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * The IAM users the latest account check found under the workspace's path with no record here,
 * and those management is revoking. A peer installation's need nothing; the admin key can revoke
 * any other with the boundary, and a user without it is left to the AWS console. A user tagged by
 * another installation can make that installation a peer, after a workspace transfer.
 */
export function FoundUsers({
  workspaceId,
  account,
  servers,
  owner,
  refresh,
}: {
  workspaceId: string;
  account: Account;
  servers: Schema["Server"][] | undefined;
  owner: boolean;
  refresh: () => Promise<void>;
}) {
  const { t } = useTranslation("backup-storage");
  const id = useId();
  if (!account.foundUsers.length) return null;
  return (
    <section className={st.part} aria-labelledby={id}>
      <h4 id={id} className={st.subheading}>
        {t(($) => $.found.title)}
      </h4>
      <p className={s.muted}>{t(($) => $.found.description)}</p>
      <ul className={st.items}>
        {account.foundUsers.map((user) => (
          <FoundUserItem
            key={user.userName}
            workspaceId={workspaceId}
            account={account}
            user={user}
            server={servers?.find((v) => v.id === user.serverId)?.name}
            owner={owner}
            refresh={refresh}
          />
        ))}
      </ul>
    </section>
  );
}

function FoundUserItem({
  workspaceId,
  account,
  user,
  server,
  owner,
  refresh,
}: {
  workspaceId: string;
  account: Account;
  user: FoundUser;
  server?: string;
  owner: boolean;
  refresh: () => Promise<void>;
}) {
  const { t } = useTranslation("backup-storage");
  const text = useBackupStorageText();
  // Both actions take themselves away, so focus moves to the user, which then says why.
  const item = useRef<HTMLLIElement>(null);
  const revoking = user.revoking;
  const path = { workspaceId, accountId: account.id };
  const self = user.installationId === account.installationId;
  const listed = account.peers.includes(user.installationId);
  // The report marks peers as of the latest check; one added since counts at once.
  const peer = user.peer || listed;
  // A peer is another installation, named by its UUID tag.
  const peerable = !user.peer && !self && uuid.test(user.installationId);
  return (
    <li ref={item} tabIndex={-1} className={st.focusTarget}>
      <span className={st.identifier}>{user.userName}</span>
      <p className={s.muted}>
        {self
          ? t(($) => $.found.self)
          : user.installationId
            ? t(($) => $.found.installation, {
                installation: user.installationId,
              })
            : t(($) => $.found.untagged)}
        {user.serverId && (
          <>
            {" · "}
            {t(($) => $.found.server, { server: server ?? user.serverId })}
          </>
        )}
      </p>
      {revoking ? (
        // Management keeps trying, and says why the last attempt failed.
        user.lastError ? (
          <p className={s.notice}>
            {t(($) => $.found.revokeFailing, {
              reason: text.failure(user.lastError),
            })}
          </p>
        ) : (
          <p>{t(($) => $.found.revoking)}</p>
        )
      ) : peer ? (
        <p>{t(($) => $.found.peer)}</p>
      ) : (
        !user.bounded && (
          <p className={s.notice}>{t(($) => $.found.unbounded)}</p>
        )
      )}
      {owner && !peer && user.bounded && (
        <ConfirmAction
          available={!revoking}
          focusAfter={item}
          label={t(($) => $.found.revoke)}
          title={t(($) => $.found.revokeTitle, { name: user.userName })}
          description={t(($) => $.found.revokeDescription)}
          run={async () => {
            await result(
              client.POST(
                `${backupStorageAccountPath}/found-users/{userName}/revoke`,
                { params: { path: { ...path, userName: user.userName } } },
              ),
            );
            await refresh();
          }}
        />
      )}
      {owner && peerable && (
        <ConfirmAction
          available={!listed}
          focusAfter={item}
          danger={false}
          label={t(($) => $.found.addPeer)}
          title={t(($) => $.found.addPeerTitle, {
            installation: user.installationId,
          })}
          description={t(($) => $.found.addPeerDescription)}
          confirm={t(($) => $.found.addPeerConfirm)}
          run={async () => {
            await result(
              client.POST(`${backupStorageAccountPath}/peers`, {
                params: { path },
                body: { installationId: user.installationId },
              }),
            );
            await refresh();
          }}
        />
      )}
    </li>
  );
}
