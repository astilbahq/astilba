import { useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { cx } from "../../../styled-system/css";
import {
  backupDestinationPath,
  backupDestinationsOptions,
} from "../../api/backups";
import { ApiError, client, result, type Schema } from "../../api/client";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { ErrorNotice, useAction } from "../../ui";
import { useBackupStorageText } from "./backup-storage-text";
import * as st from "./backup-storage.styles";
import { ConfirmAction } from "./confirm-action";

type Key = Schema["BackupStorageKey"];

/**
 * A managed destination's minted keys and their states, newest first. Its owners can rotate its
 * key without revoking its server, revoke an active key, and retry revocations or a new key's
 * making that keep failing once they have fixed the cause.
 */
export function ManagedDestination({
  workspaceId,
  destination,
  servers,
  owner,
}: {
  workspaceId: string;
  destination: Schema["BackupDestination"];
  servers: Schema["Server"][] | undefined;
  owner: boolean;
}) {
  const { t } = useTranslation("backup-storage");
  const text = useBackupStorageText();
  const queries = useQueryClient();
  const retry = useAction();
  const [retried, setRetried] = useState(false);
  const id = useId();
  const serverName = (serverId: string | null) =>
    servers?.find((v) => v.id === serverId)?.name ??
    t(($) => $.destination.unknownServer, {
      id: (serverId ?? "").slice(0, 8),
    });
  const current = destination.keys.filter((k) => k.state !== "revoked");
  const serverKeys = destination.keys.filter((k) => k.purpose === "server");
  const active = serverKeys.filter((k) => k.state === "active").length;
  const minting = serverKeys.some((k) => k.state === "minting");
  // A revoked server's keys are never replaced: they are revoked, or kept for a handover.
  const revokedServer =
    servers?.find((v) => v.id === destination.serverId)?.status === "revoked";
  // Rotating takes the button away, so focus moves to the heading, which the notice follows.
  const heading = useRef<HTMLHeadingElement>(null);
  const revoked = destination.keys.filter((k) => k.state === "revoked");
  const failing =
    !!destination.remintError ||
    destination.keys.some((k) => k.state === "revoking" && k.attempts > 0);
  const refresh = () =>
    queries.invalidateQueries({
      queryKey: backupDestinationsOptions(workspaceId).queryKey,
    });
  const keyItem = (key: Key) => (
    <KeyItem
      key={key.id}
      workspaceId={workspaceId}
      destinationId={destination.id}
      storageKey={key}
      server={serverName(key.serverId)}
      owner={owner}
      refresh={refresh}
    />
  );
  return (
    <section className={st.part} aria-labelledby={id}>
      <h5
        id={id}
        ref={heading}
        tabIndex={-1}
        className={cx(st.subheading, st.focusTarget)}
      >
        {destination.name}
      </h5>
      <p className={s.muted}>
        {destination.prefix
          ? t(($) => $.destination.locationPrefix, {
              bucket: destination.bucket,
              region: destination.region,
              prefix: destination.prefix,
            })
          : t(($) => $.destination.location, {
              bucket: destination.bucket,
              region: destination.region,
            })}{" "}
        {t(($) => $.destination.server, {
          server: serverName(destination.serverId),
        })}
      </p>
      {destination.exposed && (
        <p className={s.notice}>{t(($) => $.destination.exposed)}</p>
      )}
      {destination.remintError && (
        <p className={s.notice}>
          {t(($) => $.destination.remintFailed, {
            reason: text.failure(destination.remintError),
          })}
        </p>
      )}
      {destination.rotationRequested ? (
        <p>{t(($) => $.destination.rotationRequested)}</p>
      ) : active > 1 ? (
        <p>
          {t(($) => $.destination.rotating, {
            server: serverName(destination.serverId),
          })}
        </p>
      ) : (
        destination.rotationDueAt && (
          <p className={s.muted}>
            {Date.parse(destination.rotationDueAt) <= Date.now()
              ? t(($) => $.destination.rotationOverdue, {
                  date: text.date(destination.rotationDueAt),
                })
              : t(($) => $.destination.rotationDue, {
                  date: text.date(destination.rotationDueAt),
                })}
          </p>
        )
      )}
      {owner && (
        // Only one key in use on a server that isn't revoked, with no new one being made or asked
        // for, is rotated.
        <ConfirmAction
          available={
            active === 1 &&
            !minting &&
            !revokedServer &&
            !destination.rotationRequested
          }
          focusAfter={heading}
          danger={false}
          label={t(($) => $.destination.rotate)}
          title={t(($) => $.destination.rotateTitle)}
          description={t(($) => $.destination.rotateDescription, {
            server: serverName(destination.serverId),
          })}
          run={async () => {
            await result(
              client.POST(`${backupDestinationPath}/keys/rotate`, {
                params: {
                  path: { workspaceId, destinationId: destination.id },
                },
              }),
            ).catch(async (error: unknown) => {
              // A refusal means the keys changed since they were shown.
              if (error instanceof ApiError && error.status === 409)
                await refresh();
              throw error;
            });
            await refresh();
          }}
        />
      )}
      {current.length ? (
        <ul className={st.items}>{current.map(keyItem)}</ul>
      ) : (
        !revoked.length && <p>{t(($) => $.destination.noKeys)}</p>
      )}
      {!!revoked.length && (
        <details>
          <summary>{t(($) => $.destination.revoked)}</summary>
          <ul className={st.items}>{revoked.map(keyItem)}</ul>
        </details>
      )}
      {owner && failing && (
        <div className={s.row}>
          <Button
            type="button"
            // Keeps focus while busy, so a refusal is read beside it.
            focusableWhenDisabled
            disabled={retry.isPending}
            onClick={() =>
              retry.run(async () => {
                setRetried(false);
                await result(
                  client.POST(`${backupDestinationPath}/retry`, {
                    params: {
                      path: { workspaceId, destinationId: destination.id },
                    },
                  }),
                );
                setRetried(true);
                await refresh();
              })
            }
          >
            {retry.isPending
              ? t(($) => $.destination.retrying)
              : t(($) => $.destination.retry)}
          </Button>
          <p role="status">
            {retried ? t(($) => $.destination.retryRequested) : null}
          </p>
        </div>
      )}
      <ErrorNotice error={retry.error} />
    </section>
  );
}

function KeyItem({
  workspaceId,
  destinationId,
  storageKey: key,
  server,
  owner,
  refresh,
}: {
  workspaceId: string;
  destinationId: string;
  storageKey: Key;
  server: string;
  owner: boolean;
  refresh: () => Promise<void>;
}) {
  const { t } = useTranslation("backup-storage");
  const text = useBackupStorageText();
  // Revoking takes the key's action away, so focus moves to the key, which then says so.
  const item = useRef<HTMLLIElement>(null);
  return (
    <li ref={item} tabIndex={-1} className={st.focusTarget}>
      <div className={st.itemHeader}>
        <strong>
          {t(($) => $.key.label, {
            purpose: text.purpose(key.purpose),
            server,
          })}
        </strong>
        <span>{text.state(key.state)}</span>
      </div>
      <span className={st.identifier}>{key.userName}</span>
      {key.handedOver && <p>{t(($) => $.key.handedOver)}</p>}
      {key.state === "revoked" && key.revokedAt ? (
        <p className={s.muted}>
          {t(($) => $.key.revokedAt, { date: text.date(key.revokedAt) })}
        </p>
      ) : (
        key.activatedAt && (
          <p className={s.muted}>
            {t(($) => $.key.activated, { date: text.date(key.activatedAt) })}
          </p>
        )
      )}
      {key.lastError &&
        // Until a revocation fails, the code says why the key is being revoked.
        (key.state === "revoking" && key.attempts > 0 ? (
          <p className={s.notice}>
            {t(($) => $.key.failing, {
              count: key.attempts,
              reason: text.failure(key.lastError),
            })}
          </p>
        ) : (
          <p className={s.muted}>{text.failure(key.lastError)}</p>
        ))}
      {owner && key.state !== "revoked" && (
        <ConfirmAction
          available={key.state === "active"}
          focusAfter={item}
          label={t(($) => $.key.revoke)}
          title={t(($) => $.key.revokeTitle)}
          description={
            key.handedOver
              ? t(($) => $.key.revokeHandedOver)
              : key.purpose === "recovery"
                ? t(($) => $.key.revokeRecovery)
                : t(($) => $.key.revokeCurrent)
          }
          run={async () => {
            await result(
              client.POST(`${backupDestinationPath}/keys/{keyId}/revoke`, {
                params: {
                  path: { workspaceId, destinationId, keyId: key.id },
                },
              }),
            );
            await refresh();
          }}
        />
      )}
    </li>
  );
}
