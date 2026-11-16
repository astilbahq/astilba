import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { backupPolicyPath as policyRoot } from "../api/backups";
import { client, result, type Schema } from "../api/client";
import { publishDraft } from "../api/environment";
import {
  AlertDialogActions,
  AlertDialogClose,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogRoot,
  AlertDialogTitle,
} from "../components/alert-dialog";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Label } from "../components/label";
import { Radio } from "../components/radio";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Form, text, useAction } from "../ui";
import * as r from "./backup-recovery.styles";
import type { EnvironmentPath } from "./visual-editor";

const recoveryRoot = `${policyRoot}/recoveries/{recoveryId}` as const;

type Recovery = Schema["BackupRecoveryRecord"];
const ended: readonly Recovery["state"][] = [
  "succeeded",
  "failed",
  "cancelled",
];
const stateKeys = {
  queued: "queued",
  dispatched: "dispatched",
  running: "running",
  waiting_for_agent: "waitingForAgent",
  needs_attention: "needsAttention",
  succeeded: "succeeded",
  failed: "failed",
  cancelled: "cancelled",
} as const satisfies Record<Recovery["state"], string>;

// A duration in the largest unit that keeps it readable, in the reader's language.
function duration(ms: number, language?: string) {
  const minutes = Math.max(1, Math.round(ms / 60_000));
  const [value, unit] =
    minutes < 120
      ? [minutes, "minute"]
      : minutes < 2880
        ? [Math.round(minutes / 60), "hour"]
        : [Math.round(minutes / 1440), "day"];
  return new Intl.NumberFormat(language, {
    style: "unit",
    unit,
    unitDisplay: "long",
  }).format(value);
}

function size(bytes: number, language?: string) {
  const units = ["byte", "kilobyte", "megabyte", "gigabyte", "terabyte"];
  let value = bytes,
    unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit++;
  }
  return new Intl.NumberFormat(language, {
    style: "unit",
    unit: units[unit],
    unitDisplay: "short",
    maximumFractionDigits: 1,
  }).format(value);
}

/**
 * Restores a snapshot of a policy whose server was revoked, onto the environment's current server,
 * and cuts over to or discards what each restore created. Only snapshots management received from
 * the server are offered, at the time it received them; what the server claimed is labelled so.
 */
export function RevokedServerRecovery({
  path,
  policy,
  server,
  runs,
  revision,
  owner,
  dirty,
  load,
}: {
  path: EnvironmentPath;
  policy: Schema["BackupPolicy"];
  server?: Schema["Server"];
  runs: Schema["BackupRun"][];
  revision: Schema["Revision"];
  owner: boolean;
  dirty: boolean;
  load: (revision: Schema["Revision"]) => boolean;
}) {
  const { t, i18n } = useTranslation("backup-recovery");
  const queries = useQueryClient();
  const action = useAction({ recentAuth: true });
  const [message, setMessage] = useState("");
  const p = { ...path, policyId: policy.id };
  const date = (value: string) =>
    new Date(value).toLocaleString(i18n.resolvedLanguage);
  const receipts = useQuery({
    queryKey: ["backup-receipts", p],
    // Only owners choose a snapshot.
    enabled: owner,
    queryFn: () =>
      result(client.GET(`${policyRoot}/receipts`, { params: { path: p } })),
  });
  const recoveries = useQuery({
    queryKey: ["backup-recoveries", p],
    queryFn: () =>
      result(client.GET(`${policyRoot}/recoveries`, { params: { path: p } })),
    refetchInterval: (query) =>
      query.state.data?.some(
        (v) =>
          !ended.includes(v.state) ||
          (!!v.discard && !ended.includes(v.discard.state)),
      )
        ? 5000
        : false,
  });
  // A retried request reuses its key, so it can't start a second restore.
  const attempts = useRef(new Map<string, string>());
  const name = server?.name ?? t(($) => $.revoked.unknownServer);
  const revokedAt = server?.revokedAt;
  const method = (value: string) =>
    value === "postgres-logical"
      ? t(($) => $.method.postgresLogical)
      : value === "quiesced-files"
        ? t(($) => $.method.quiescedFiles)
        : value;
  return (
    <section className={s.stack} aria-label={t(($) => $.revoked.title)}>
      <h4 className={r.heading}>{t(($) => $.revoked.title)}</h4>
      <p className={s.notice}>
        {revokedAt
          ? t(($) => $.revoked.since, { server: name, date: date(revokedAt) })
          : t(($) => $.revoked.sinceUnknown, { server: name })}
      </p>
      <p>{t(($) => $.revoked.frozen)}</p>
      <p role="status">{message}</p>
      {owner &&
        (receipts.isPending ? (
          <p role="status">{t(($) => $.picker.loading)}</p>
        ) : !receipts.data ? (
          <ErrorNotice
            error={receipts.error}
            message={t(($) => $.picker.loadFailed)}
          />
        ) : receipts.data.length === 0 ? (
          <p>{t(($) => $.picker.none)}</p>
        ) : (
          <Form
            action={action}
            label={t(($) => $.picker.legend)}
            onSubmit={(d, form) =>
              action.run(async () => {
                setMessage("");
                const snapshot = text(d, "snapshot");
                const requestKey =
                  attempts.current.get(snapshot) ?? crypto.randomUUID();
                attempts.current.set(snapshot, requestKey);
                await result(
                  client.POST(`${policyRoot}/recoveries`, {
                    params: { path: p },
                    body: { snapshot, requestKey, acknowledged: true },
                  }),
                );
                attempts.current.delete(snapshot);
                // Each restore is acknowledged afresh.
                form.reset();
                setMessage(t(($) => $.picker.started));
                await queries.invalidateQueries({
                  queryKey: ["backup-recoveries", p],
                });
              })
            }
          >
            <fieldset className={r.choices}>
              <legend className={r.legend}>{t(($) => $.picker.legend)}</legend>
              {receipts.data.map((receipt) => {
                const run = runs.find((v) => v.snapshot === receipt.snapshot);
                const before =
                  revokedAt &&
                  Date.parse(revokedAt) - Date.parse(receipt.firstReportedAt);
                return (
                  <label key={receipt.snapshot} className={r.choice}>
                    <Radio name="snapshot" value={receipt.snapshot} required />
                    <span className={r.choiceText}>
                      <span>
                        {before && before > 0
                          ? t(($) => $.picker.recordedBefore, {
                              date: date(receipt.firstReportedAt),
                              duration: duration(before, i18n.resolvedLanguage),
                            })
                          : t(($) => $.picker.recorded, {
                              date: date(receipt.firstReportedAt),
                            })}
                      </span>
                      <span className={r.identifier}>{receipt.snapshot}</span>
                      {run && (
                        <span className={s.muted}>
                          {t(($) => $.picker.claimed, {
                            date: date(run.startedAt),
                          })}
                        </span>
                      )}
                      {run?.manifest && (
                        <span className={s.muted}>
                          {t(($) => $.picker.unverified, {
                            size: size(
                              run.manifest.bytes,
                              i18n.resolvedLanguage,
                            ),
                            method: method(run.manifest.method),
                            image: run.manifest.image,
                          })}
                        </span>
                      )}
                    </span>
                  </label>
                );
              })}
            </fieldset>
            <Label className={r.choice}>
              <Checkbox name="acknowledged" required />
              {t(($) => $.picker.acknowledge)}
            </Label>
            <ErrorNotice error={action.error} />
            <BusyButton busy={action.isPending}>
              {t(($) => $.picker.restore)}
            </BusyButton>
          </Form>
        ))}
      {recoveries.isError && (
        <ErrorNotice
          error={recoveries.error}
          message={t(($) => $.restores.loadFailed)}
        />
      )}
      {!!recoveries.data?.length && (
        <>
          <h5 className={r.heading}>{t(($) => $.restores.title)}</h5>
          <ol className={s.list}>
            {recoveries.data.map((recovery) => (
              <RecoveryItem
                key={recovery.id}
                path={path}
                policyId={policy.id}
                recovery={recovery}
                revision={revision}
                owner={owner}
                dirty={dirty}
                load={load}
                announce={setMessage}
              />
            ))}
          </ol>
        </>
      )}
    </section>
  );
}

function RecoveryItem({
  path,
  policyId,
  recovery,
  revision,
  owner,
  dirty,
  load,
  announce,
}: {
  path: EnvironmentPath;
  policyId: string;
  recovery: Recovery;
  revision: Schema["Revision"];
  owner: boolean;
  dirty: boolean;
  load: (revision: Schema["Revision"]) => boolean;
  announce: (message: string) => void;
}) {
  const { t, i18n } = useTranslation("backup-recovery");
  const queries = useQueryClient();
  const action = useAction({ recentAuth: true });
  const p = { ...path, policyId, recoveryId: recovery.id };
  // A discard holds the volume while it runs, and a succeeded one deleted it: neither a cutover
  // nor another discard is offered then, as the server refuses both.
  const discard = recovery.discard;
  const pending = !!discard && !ended.includes(discard.state);
  const failed =
    !!discard &&
    ended.includes(discard.state) &&
    discard.state !== "succeeded" &&
    !recovery.targetDeleted;
  const held = pending || recovery.targetDeleted;
  // Once a discard is confirmed its button goes, so focus moves to the volume's status, which
  // announces each change to the discard itself.
  const volume = useRef<HTMLParagraphElement>(null);
  const focusVolume = useRef(false);
  useEffect(() => {
    if (held && focusVolume.current) {
      focusVolume.current = false;
      volume.current?.focus();
    }
  }, [held]);
  // A refusal means the server's view moved on, as when another owner started a discard.
  const refresh = () =>
    queries.invalidateQueries({
      queryKey: ["backup-recoveries", { ...path, policyId }],
    });
  const succeeded = recovery.state === "succeeded" && !recovery.targetDeleted;
  return (
    <li className={s.stack}>
      <p>
        {t(($) => $.restores.item, {
          snapshot: recovery.snapshot.slice(0, 12),
          date: new Date(recovery.firstReportedAt).toLocaleString(
            i18n.resolvedLanguage,
          ),
          state: t(($) => $.restores.state[stateKeys[recovery.state]]),
        })}
      </p>
      {recovery.note && <p>{recovery.note}</p>}
      {(held || failed || recovery.state === "succeeded") && (
        <p ref={volume} tabIndex={-1} role="status" className={s.muted}>
          {recovery.targetDeleted
            ? t(($) => $.restores.discarded)
            : pending
              ? discard.state === "needs_attention"
                ? t(($) => $.restores.discardAttention, { note: discard.note })
                : t(($) => $.restores.discardPending)
              : failed
                ? t(($) => $.restores.discardFailed, {
                    volume: recovery.targetId,
                    note: discard.note,
                  })
                : t(($) => $.restores.target, { volume: recovery.targetId })}
        </p>
      )}
      <ErrorNotice error={action.error} />
      {owner && succeeded && !held && (
        <>
          <p>{t(($) => $.restores.cutoverHint)}</p>
          {dirty && (
            <p className={s.muted}>{t(($) => $.restores.cutoverDirty)}</p>
          )}
        </>
      )}
      {owner && !held && ended.includes(recovery.state) && (
        <div className={s.row}>
          {succeeded && (
            <Button
              type="button"
              variant="primary"
              disabled={action.isPending || dirty}
              onClick={() =>
                action.run(async () => {
                  // Cleared first, so the same outcome twice is announced twice.
                  announce("");
                  const next = await result(
                    client.POST(`${recoveryRoot}/cutover`, {
                      params: { path: p },
                      body: { baseRevisionId: revision.id },
                    }),
                  ).catch(async (error: unknown) => {
                    await refresh();
                    throw error;
                  });
                  await publishDraft(queries, path, next, revision.id);
                  announce(
                    load(next)
                      ? t(($) => $.restores.cutoverSaved)
                      : t(($) => $.restores.cutoverConflict),
                  );
                })
              }
            >
              {action.isPending
                ? t(($) => $.restores.preparing)
                : t(($) => $.restores.prepareCutover)}
            </Button>
          )}
          <DiscardTarget
            path={p}
            recovery={recovery}
            refresh={async (queued) => {
              focusVolume.current = queued;
              await refresh();
            }}
          />
        </div>
      )}
    </li>
  );
}

function DiscardTarget({
  path,
  recovery,
  refresh,
}: {
  path: EnvironmentPath & { policyId: string; recoveryId: string };
  recovery: Recovery;
  /** Reloads the restores, once queued moving focus to the volume's status. */
  refresh: (queued: boolean) => Promise<void>;
}) {
  const { t } = useTranslation("backup-recovery");
  const action = useAction({ recentAuth: true });
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  return (
    <>
      <Button
        ref={trigger}
        type="button"
        variant="danger"
        onClick={() => setOpen(true)}
      >
        {t(($) => $.restores.discard)}
      </Button>
      <AlertDialogRoot
        open={open}
        onOpenChange={(next) => {
          if (!next && !action.isPending) setOpen(false);
        }}
      >
        <AlertDialogContent finalFocus={trigger}>
          <AlertDialogTitle>
            {t(($) => $.restores.discardTitle)}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t(($) => $.restores.discardDescription, {
              volume: recovery.targetId,
            })}
          </AlertDialogDescription>
          <ErrorNotice error={action.error} />
          <AlertDialogActions>
            <AlertDialogClose
              render={
                <Button disabled={action.isPending}>
                  {t(($) => $.restores.cancel)}
                </Button>
              }
            />
            <Button
              type="button"
              variant="danger"
              disabled={action.isPending}
              onClick={(event) =>
                action.run(async () => {
                  await result(
                    client.DELETE(`${recoveryRoot}/target`, {
                      params: { path },
                    }),
                  ).catch(async (error: unknown) => {
                    await refresh(false);
                    throw error;
                  });
                  setOpen(false);
                  await refresh(true);
                }, trigger.current ?? event.currentTarget)
              }
            >
              {action.isPending
                ? t(($) => $.restores.discarding)
                : t(($) => $.restores.discardConfirm)}
            </Button>
          </AlertDialogActions>
        </AlertDialogContent>
      </AlertDialogRoot>
    </>
  );
}

/** Labels a revoked server's own report of its runs, which nothing has checked. */
export function RevokedServerHistory() {
  const { t } = useTranslation("backup-recovery");
  return <p className={s.muted}>{t(($) => $.revoked.history)}</p>;
}

/** A persistent warning on a policy whose destination's keys a revoked server was given. */
export function ExposedDestinationNotice() {
  const { t } = useTranslation("backup-recovery");
  return <p className={s.notice}>{t(($) => $.exposed.policy)}</p>;
}

/** The warning and acknowledgement for creating a policy on an exposed destination. */
export function ExposureAcknowledgement() {
  const { t } = useTranslation("backup-recovery");
  return (
    <>
      <p className={s.notice}>{t(($) => $.exposed.warning)}</p>
      <Label className={r.choice}>
        <Checkbox name="exposureAcknowledged" required />
        {t(($) => $.exposed.acknowledge)}
      </Label>
    </>
  );
}
