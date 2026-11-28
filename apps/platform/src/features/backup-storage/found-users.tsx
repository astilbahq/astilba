import { useId, useRef } from "react";
import { useTranslation } from "react-i18next";
import { backupStorageAccountPath } from "../../api/backups";
import { ApiError, client, result, type Schema } from "../../api/client";
import * as s from "../../styles";
import { useBackupStorageText } from "./backup-storage-text";
import * as st from "./backup-storage.styles";
import { ConfirmAction } from "./confirm-action";

type Account = Schema["BackupStorageAccount"];
type FoundUser = Schema["BackupStorageFoundUser"];

// Management tags users with an installation's UUID in lower case, and takes no other form as a
// peer's.
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

// A found user's identity is the nil UUID until a check records it: for one management is
// revoking, which the check no longer reports, or in a report from before identities were
// recorded.
const unrecorded = "00000000-0000-0000-0000-000000000000";

// After a refusal the latest check may show the user otherwise, so the list is shown again, and
// the dialog, which stays mounted, says why.
async function refusal(error: unknown, refresh: () => Promise<void>) {
  if (error instanceof ApiError && error.status === 409) await refresh();
  throw error;
}

/**
 * The IAM users the latest account check found under the workspace's path with no record here,
 * and those management is revoking. A peer installation's user raises an alert until the owner
 * acknowledges it by name, since a leaked admin key can make one with the peer's tag. The admin
 * key can revoke any user with the boundary but an acknowledged peer's, and a user without it is
 * left to the AWS console. A user tagged by another installation can make that installation a
 * peer, after a workspace transfer.
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
  const recorded = user.id !== unrecorded;
  const keys = user.accessKeyIds.join(", ");
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
      {/* What an acknowledgement covers, once a check has recorded it. */}
      {user.bounded && recorded && (
        <p className={s.muted}>
          {t(($) => $.found.userId, { id: user.userId })}
          {" · "}
          {user.accessKeyIds.length
            ? t(($) => $.found.accessKeys, { keys })
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
        ) : recorded ? (
          <p className={s.notice}>{t(($) => $.found.peerUnacknowledged)}</p>
        ) : (
          <p className={s.notice}>{t(($) => $.found.peerUnrecorded)}</p>
        )
      ) : (
        !user.bounded && (
          <p className={s.notice}>{t(($) => $.found.unbounded)}</p>
        )
      )}
      {/* The actions stay mounted while the owner can act, so a dialog outlives a refusal that
          changes what the list shows. An acknowledged peer's user is that installation's. */}
      {owner && (
        <ConfirmAction
          available={user.bounded && !revoking && !user.acknowledgedAt}
          focusAfter={item}
          label={t(($) => $.found.revoke)}
          title={t(($) => $.found.revokeTitle, { name: user.userName })}
          description={
            peer
              ? t(($) => $.found.revokePeerDescription, {
                  installation: user.installationId,
                })
              : t(($) => $.found.revokeDescription)
          }
          run={async () => {
            await result(
              client.POST(
                `${backupStorageAccountPath}/found-users/{userName}/revoke`,
                { params: { path: { ...path, userName: user.userName } } },
              ),
            ).catch((error: unknown) => refusal(error, refresh));
            await refresh();
          }}
        />
      )}
      {owner && (
        // The acknowledgement covers this user as shown: management refuses it if the latest
        // check found another IAM user under the name, or other access keys.
        <ConfirmAction
          available={
            peer &&
            user.bounded &&
            recorded &&
            !revoking &&
            !user.acknowledgedAt
          }
          focusAfter={item}
          danger={false}
          label={t(($) => $.found.acknowledge)}
          title={t(($) => $.found.acknowledgeTitle, {
            name: user.userName,
            installation: user.installationId,
          })}
          description={
            user.accessKeyIds.length
              ? t(($) => $.found.acknowledgeDescription, {
                  id: user.userId,
                  keys,
                })
              : t(($) => $.found.acknowledgeDescriptionNoKeys, {
                  id: user.userId,
                })
          }
          confirm={t(($) => $.found.acknowledgeConfirm)}
          run={async () => {
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
            ).catch((error: unknown) => refusal(error, refresh));
            await refresh();
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
