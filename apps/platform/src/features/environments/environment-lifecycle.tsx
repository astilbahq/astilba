import {
  type QueryClient,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { type FormEvent, useId, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { ApiError, client, result, type Schema } from "../../api/client";
import {
  type EnvironmentPath,
  previewsOptions,
  volumesOptions,
} from "../../api/environment";
import { environmentsOptions } from "../../api/environments";
import {
  AlertDialogActions,
  AlertDialogClose,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogRoot,
  AlertDialogTitle,
} from "../../components/alert-dialog";
import { Button } from "../../components/button";
import { Input } from "../../components/input";
import { Label } from "../../components/label";
import * as s from "../../styles";
import { useAction } from "../../ui";
import * as e from "./environment.styles";

export type LifecycleChange = "retire" | "restore" | "purge";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}";

const previewStates = [
  "approval_required",
  "queued",
  "building",
  "deploying",
  "ready",
  "failed",
  "closing",
  "deleting",
  "deleted",
  "blocked",
] as const;
const isPreviewState = (
  state: string,
): state is (typeof previewStates)[number] =>
  (previewStates as readonly string[]).includes(state);

const hasCode = (error: unknown, code: string) =>
  error instanceof ApiError && error.detail.code === code;

// Only the typed name is the owner's to correct. Any other invalid input, such as a deployment
// plan the server cannot build, is a refusal.
const nameMismatch = (error: unknown) =>
  error instanceof ApiError &&
  error.detail.code === "invalid_input" &&
  !!error.detail.fields?.includes("confirmName");

// The project's environment list and each environment's own copy of it.
const environmentQueries = (path: EnvironmentPath) =>
  environmentsOptions(path.workspaceId, path.projectId).queryKey;

// Everything cached about the environment may have changed: retirement revokes its credentials
// and turns off its policies, and every write is now refused.
function publishEnvironment(
  queries: QueryClient,
  path: EnvironmentPath,
  next: Schema["Environment"],
) {
  const list = environmentQueries(path);
  queries.setQueriesData<Schema["Environment"][]>({ queryKey: list }, (items) =>
    items?.map((item) => (item.id === next.id ? next : item)),
  );
  void queries.invalidateQueries({
    predicate: ({ queryKey }) =>
      queryKey.some(
        (part) =>
          part === next.id ||
          (typeof part === "object" &&
            part !== null &&
            "environmentId" in part &&
            part.environmentId === next.id),
      ),
  });
  void queries.invalidateQueries({ queryKey: list });
}

// Codes and refusal reasons select the copy, never the server's English, so a reason this
// dashboard does not know yet reads as a general refusal.
function useLifecycleErrorText() {
  const { t } = useTranslation("environment");
  const common = useTranslation("common").t;
  const refusal = (field: string | undefined) => {
    switch (field) {
      case "retirement.active_operation":
        return t(($) => $.lifecycle.refusal.activeOperation);
      case "retirement.server_busy":
        return t(($) => $.lifecycle.refusal.serverBusy);
      case "retirement.upgrade":
        return t(($) => $.lifecycle.refusal.upgrade);
      case "retirement.cutover":
        return t(($) => $.lifecycle.refusal.cutover);
      case "retirement.cross_host":
        return t(($) => $.lifecycle.refusal.crossHost);
      case "retirement.replacement":
        return t(($) => $.lifecycle.refusal.replacement);
      case "retirement.agent_update":
        return t(($) => $.lifecycle.refusal.agentUpdate);
      case "retirement.server_transfer":
        return t(($) => $.lifecycle.refusal.serverTransfer);
      case "retirement.state":
        return t(($) => $.lifecycle.refusal.state);
      case "retirement.volumes":
        return t(($) => $.lifecycle.refusal.volumes);
      case "retirement.purge_agent_update":
        return t(($) => $.lifecycle.refusal.purgeAgentUpdate);
      case "retirement.previews":
        return t(($) => $.lifecycle.refusal.previews);
      default:
        return t(($) => $.lifecycle.refusal.unknown);
    }
  };
  return (error: unknown) => {
    if (!(error instanceof ApiError)) return common(($) => $.unexpectedError);
    switch (error.detail.code) {
      case "invalid_input":
        return nameMismatch(error)
          ? t(($) => $.lifecycle.nameMismatch)
          : t(($) => $.lifecycle.refusal.unknown);
      case "forbidden":
        return t(($) => $.lifecycle.forbidden);
      case "mfa_required":
        return t(($) => $.lifecycle.mfa);
      case "email_verification_required":
        return t(($) => $.lifecycle.verifyEmail);
      case "subscription_required":
        return t(($) => $.lifecycle.subscription);
      case "recent_auth_required":
        return t(($) => $.lifecycle.recentAuth);
      case "not_found":
        return t(($) => $.lifecycle.unavailable);
      case "conflict":
        return refusal(error.detail.fields?.[0]);
      default:
        return common(($) => $.requestFailed);
    }
  };
}

export function RetireSection({
  onRetire,
}: {
  onRetire: (trigger: HTMLButtonElement) => void;
}) {
  const { t } = useTranslation("environment");
  const heading = useId();
  return (
    <section className={s.panel} aria-labelledby={heading}>
      <h2 id={heading}>{t(($) => $.lifecycle.retireTitle)}</h2>
      <p className={e.muted}>{t(($) => $.lifecycle.retireDescription)}</p>
      <div>
        <Button
          variant="danger"
          onClick={(event) => onRetire(event.currentTarget)}
        >
          {t(($) => $.lifecycle.retire)}
        </Button>
      </div>
    </section>
  );
}

// Retiring, restoring and purging all need the environment's exact name and a recent identity
// confirmation, which the shared step-up dialog asks for over this one. The typed name survives
// it and any refused request.
export function LifecycleDialog({
  change,
  open,
  environment,
  path,
  onClose,
  onChanged,
  finalFocus,
}: {
  change: LifecycleChange;
  open: boolean;
  environment: Schema["Environment"];
  path: EnvironmentPath;
  onClose: () => void;
  onChanged: (next: Schema["Environment"]) => void;
  finalFocus: () => HTMLElement | null;
}) {
  const { t } = useTranslation("environment");
  const message = useLifecycleErrorText();
  const queries = useQueryClient();
  // Each always needs recent authentication, so a stale session confirms before the request.
  const action = useAction({ recentAuth: true });
  const [name, setName] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const volumeItem = useRef<HTMLLIElement>(null);
  const previewItem = useRef<HTMLLIElement>(null);
  const formId = useId();
  const inputId = useId();
  const hintId = useId();
  const errorId = useId();
  const purge = change === "purge";
  // A purge names exactly the volumes it deletes: the owner confirms the list shown here, which
  // the banner behind the dialog shows too, and the request sends that list and no other.
  const volumes = useQuery({ ...volumesOptions(path), enabled: purge });
  const kept = volumes.data?.filter((volume) => !volume.deleted);
  // A preview template's previews hold copies of its secrets until each is deleted, and the server
  // refuses its purge until then. The list refreshes while the dialog is open.
  const template = purge && environment.purpose === "preview_template";
  const previews = useQuery({ ...previewsOptions(path), enabled: template });
  // Only a preview that was allocated an environment holds copies, as the server counts them.
  const pending = previews.data?.filter(
    (preview) => !!preview.environmentId && preview.state !== "deleted",
  );
  const ready =
    name === environment.name &&
    (!purge || !!kept) &&
    (!template || pending?.length === 0);
  const volumesFailed = purge && !kept && volumes.isError;
  const previewsFailed = template && !pending && previews.isError;
  const mismatch = nameMismatch(action.error);
  const dismiss = () => {
    action.reset();
    setName("");
    onClose();
  };
  const request = (confirmName: string, volumeIds: string[]) => {
    switch (change) {
      case "retire":
        return client.POST(`${root}/retire`, {
          params: { path },
          body: { confirmName },
        });
      case "restore":
        return client.POST(`${root}/restore`, {
          params: { path },
          body: { confirmName },
        });
      case "purge":
        return client.POST(`${root}/purge`, {
          params: { path },
          body: { confirmName, volumeIds },
        });
    }
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!ready || action.isPending) return;
    const volumeIds = kept?.map((volume) => volume.id) ?? [];
    action.run(async () => {
      try {
        const next = await result(request(name, volumeIds));
        publishEnvironment(queries, path, next);
        onChanged(next);
        dismiss();
      } catch (error) {
        if (nameMismatch(error)) input.current?.focus();
        // A refusal or a name mismatch can come from a change made elsewhere, such as another
        // owner's retirement or rename, so the page behind the dialog catches up with it. The
        // typed name stays for another try.
        if (hasCode(error, "conflict") || nameMismatch(error))
          void queries.invalidateQueries({
            queryKey: environmentQueries(path),
          });
        // The kept volumes may have changed as well, and the owner confirms the current list. A
        // template's previews show again what each one waits for.
        if (purge && hasCode(error, "conflict")) {
          void queries.invalidateQueries({
            queryKey: volumesOptions(path).queryKey,
          });
          if (template)
            void queries.invalidateQueries({
              queryKey: previewsOptions(path).queryKey,
            });
        }
        throw error;
      }
    });
  };
  const nameComponents = { name: <bdi>{environment.name}</bdi> };
  return (
    <AlertDialogRoot
      open={open}
      onOpenChange={(next) => {
        if (!next && !action.isPending) dismiss();
      }}
    >
      <AlertDialogContent
        initialFocus={() => input.current ?? true}
        finalFocus={finalFocus}
      >
        <AlertDialogTitle ref={title} tabIndex={-1} className={e.dialogTitle}>
          {change === "retire" ? (
            <Trans
              t={t}
              i18nKey={($) => $.lifecycle.retireDialogTitle}
              components={nameComponents}
            />
          ) : change === "restore" ? (
            <Trans
              t={t}
              i18nKey={($) => $.lifecycle.restoreDialogTitle}
              components={nameComponents}
            />
          ) : (
            <Trans
              t={t}
              i18nKey={($) => $.lifecycle.purgeDialogTitle}
              components={nameComponents}
            />
          )}
        </AlertDialogTitle>
        {/* The list is part of the description, so it is read with the dialog. */}
        <AlertDialogDescription
          render={<div />}
          className={e.dialogDescription}
        >
          {change === "retire" ? (
            <>
              <p>{t(($) => $.lifecycle.retireDialogDescription)}</p>
              <ul className={e.effects}>
                <li>{t(($) => $.lifecycle.retireEffects.routes)}</li>
                <li>{t(($) => $.lifecycle.retireEffects.services)}</li>
                <li>{t(($) => $.lifecycle.retireEffects.volumes)}</li>
                <li>{t(($) => $.lifecycle.retireEffects.credentials)}</li>
                <li>{t(($) => $.lifecycle.retireEffects.policies)}</li>
                {environment.purpose === "preview_template" && (
                  <li>{t(($) => $.lifecycle.retireEffects.previews)}</li>
                )}
                <li>{t(($) => $.lifecycle.retireEffects.kept)}</li>
              </ul>
            </>
          ) : change === "restore" ? (
            <>
              <p>{t(($) => $.lifecycle.restoreDialogDescription)}</p>
              <ul className={e.effects}>
                <li>{t(($) => $.lifecycle.restoreEffects.nothingDeployed)}</li>
                <li>{t(($) => $.lifecycle.restoreEffects.staysOff)}</li>
                <li>{t(($) => $.lifecycle.restoreEffects.volumes)}</li>
              </ul>
            </>
          ) : (
            <>
              <p>{t(($) => $.lifecycle.purgeDialogDescription)}</p>
              <ul className={e.effects}>
                {/* A retry focuses this item, so focus stays in the dialog on what loads. */}
                <li ref={volumeItem} tabIndex={-1} className={e.recoveryTarget}>
                  {kept ? (
                    kept.length ? (
                      <>
                        {t(($) => $.lifecycle.purgeEffects.volumes)}
                        <ul className={e.purgeItems}>
                          {kept.map((volume) => (
                            <li key={volume.id} className={e.code}>
                              <bdi>{volume.name}</bdi>
                            </li>
                          ))}
                        </ul>
                      </>
                    ) : (
                      t(($) => $.lifecycle.purgeEffects.noVolumes)
                    )
                  ) : volumes.isError ? (
                    t(($) => $.lifecycle.status.volumesFailed)
                  ) : (
                    t(($) => $.lifecycle.status.volumesLoading)
                  )}
                </li>
                {/* Whether the server is revoked only shows once the purge is accepted. */}
                {!!kept?.length && (
                  <li>{t(($) => $.lifecycle.purgeEffects.revoked)}</li>
                )}
                {template && (
                  <li
                    ref={previewItem}
                    tabIndex={-1}
                    className={e.recoveryTarget}
                  >
                    {pending ? (
                      pending.length ? (
                        <>
                          {t(($) => $.lifecycle.purgeEffects.previews)}
                          <ul className={e.purgeItems}>
                            {pending.map((preview) => {
                              const known = preview.state;
                              // A state this dashboard does not know yet reads as the server
                              // names it.
                              const state = isPreviewState(known)
                                ? t(($) => $.lifecycle.previewStates[known])
                                : known.replaceAll("_", " ");
                              return (
                                <li key={preview.id}>
                                  {preview.note
                                    ? t(
                                        ($) =>
                                          $.lifecycle.purgeEffects
                                            .previewWithNote,
                                        {
                                          number: preview.number,
                                          state,
                                          note: preview.note,
                                        },
                                      )
                                    : t(
                                        ($) => $.lifecycle.purgeEffects.preview,
                                        { number: preview.number, state },
                                      )}
                                </li>
                              );
                            })}
                          </ul>
                        </>
                      ) : (
                        t(($) => $.lifecycle.purgeEffects.noPreviews)
                      )
                    ) : previews.isError ? (
                      t(($) => $.lifecycle.status.previewsFailed)
                    ) : (
                      t(($) => $.lifecycle.status.previewsLoading)
                    )}
                  </li>
                )}
                <li>{t(($) => $.lifecycle.purgeEffects.secrets)}</li>
                <li>{t(($) => $.lifecycle.purgeEffects.kept)}</li>
                <li>{t(($) => $.lifecycle.purgeEffects.backups)}</li>
              </ul>
            </>
          )}
        </AlertDialogDescription>
        {/* The purge cannot be confirmed without these lists. A failed read of the volumes is not
            retried on its own, and the previews' next poll is seconds away, so either can be asked
            for again. Each read's own progress disables the button, not the previews' polling. */}
        {(volumesFailed || previewsFailed) && (
          <div>
            <Button
              size="sm"
              disabled={
                (volumesFailed && volumes.isFetching) ||
                (previewsFailed && previews.isFetching)
              }
              onClick={() => {
                (volumesFailed ? volumeItem : previewItem).current?.focus();
                if (volumesFailed) void volumes.refetch();
                if (previewsFailed) void previews.refetch();
              }}
            >
              {t(($) => $.retry)}
            </Button>
          </div>
        )}
        <form id={formId} className={s.stack} onSubmit={submit}>
          <div className={e.confirmField}>
            <Label htmlFor={inputId}>
              {t(($) => $.lifecycle.confirmLabel)}
            </Label>
            <p id={hintId} className={e.muted}>
              <Trans
                t={t}
                i18nKey={($) => $.lifecycle.confirmHint}
                components={{
                  name: <bdi className={e.code}>{environment.name}</bdi>,
                }}
              />
            </p>
            <Input
              ref={input}
              id={inputId}
              name="confirmName"
              value={name}
              required
              autoComplete="off"
              spellCheck={false}
              dir="auto"
              aria-invalid={mismatch || undefined}
              aria-describedby={action.error ? `${hintId} ${errorId}` : hintId}
              onChange={(event) => setName(event.currentTarget.value)}
            />
          </div>
          {action.error && (
            <p id={errorId} className={s.error} role="alert">
              {message(action.error)}
            </p>
          )}
        </form>
        <AlertDialogActions>
          <AlertDialogClose
            render={
              <Button disabled={action.isPending}>
                {t(($) => $.lifecycle.cancel)}
              </Button>
            }
          />
          <Button
            type="submit"
            form={formId}
            variant={change === "restore" ? "primary" : "danger"}
            disabled={!ready || action.isPending}
          >
            {change === "retire"
              ? action.isPending
                ? t(($) => $.lifecycle.retiring)
                : t(($) => $.lifecycle.retire)
              : change === "restore"
                ? action.isPending
                  ? t(($) => $.lifecycle.restoring)
                  : t(($) => $.lifecycle.restore)
                : action.isPending
                  ? t(($) => $.lifecycle.purging)
                  : t(($) => $.lifecycle.purge)}
          </Button>
        </AlertDialogActions>
      </AlertDialogContent>
    </AlertDialogRoot>
  );
}
