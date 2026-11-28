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

// Management tags users with an installation's UUID in lower case, and takes no other form as a
// peer's.
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/**
 * The IAM users the latest account check found under the workspace's path with no record here,
 * and those management is revoking. A peer installation's user raises an alert until the owner
 * acknowledges it by name, since a leaked admin key can make one with the peer's tag; the admin
 * key can revoke any other with the boundary, and a user without it is left to the AWS console. A
 * user tagged by another installation can make that installation a peer, after a workspace
 * transfer.
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
  // Management marks peers by the account's peers as they are now.
  const peer = user.peer;
  // A peer is another installation, named by its UUID tag. Adding one takes the button away
  // and leaves its dialog to close.
  const peerable = !self && uuid.test(user.installationId);
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
      {/* A user being revoked that the latest check didn't list has no keys to show. */}
      {user.bounded && !revoking && (
        <p className={s.muted}>
          {user.accessKeyIds.length
            ? t(($) => $.found.accessKeys, {
                keys: user.accessKeyIds.join(", "),
              })
            : t(($) => $.found.noAccessKeys)}
        </p>
      )}
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
      ) : peer && user.bounded ? (
        user.acknowledgedAt ? (
          <p>
            {t(($) => $.found.peer, {
              date: text.date(user.acknowledgedAt),
            })}
          </p>
        ) : (
          <p className={s.notice}>{t(($) => $.found.peerUnacknowledged)}</p>
        )
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
      {owner && peer && user.bounded && !revoking && (
        // The acknowledgement covers this user as shown: management refuses it if the latest
        // check found another IAM user under the name, or other access keys, and the list is
        // then shown again as the check found it.
        <ConfirmAction
          available={!user.acknowledgedAt}
          focusAfter={item}
          danger={false}
          label={t(($) => $.found.acknowledge)}
          title={t(($) => $.found.acknowledgeTitle, {
            name: user.userName,
            installation: user.installationId,
          })}
          description={t(($) => $.found.acknowledgeDescription, {
            keys: user.accessKeyIds.length
              ? user.accessKeyIds.join(", ")
              : t(($) => $.found.noAccessKeys),
          })}
          confirm={t(($) => $.found.acknowledgeConfirm)}
          run={async () => {
            try {
              await result(
                client.POST(
                  `${backupStorageAccountPath}/found-users/{userName}/acknowledge`,
                  {
                    params: { path: { ...path, userName: user.userName } },
                    body: {
                      userId: user.userId,
                      accessKeyIds: user.accessKeyIds,
                    },
                  },
                ),
              );
            } finally {
              await refresh();
            }
          }}
        />
      )}
      {owner && peerable && (
        <ConfirmAction
          available={!peer}
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
