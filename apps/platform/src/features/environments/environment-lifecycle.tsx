import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useId, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { ApiError, client, result, type Schema } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
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

export type LifecycleChange = "retire" | "restore";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}";

const hasCode = (error: unknown, code: string) =>
  error instanceof ApiError && error.detail.code === code;

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
      case "retirement.agent_update":
        return t(($) => $.lifecycle.refusal.agentUpdate);
      case "retirement.server_transfer":
        return t(($) => $.lifecycle.refusal.serverTransfer);
      case "retirement.state":
        return t(($) => $.lifecycle.refusal.state);
      case "environment.state":
        return t(($) => $.lifecycle.refusal.inactive);
      default:
        return t(($) => $.lifecycle.refusal.unknown);
    }
  };
  return (error: unknown) => {
    if (!(error instanceof ApiError)) return common(($) => $.unexpectedError);
    switch (error.detail.code) {
      case "invalid_input":
        return t(($) => $.lifecycle.nameMismatch);
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

// Retiring and restoring both need the environment's exact name and a recent identity
// confirmation, which the shared step-up dialog asks for over this one. The typed name
// survives it and any refused request.
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
  // Both always need recent authentication, so a stale session confirms before the request.
  const action = useAction({ recentAuth: true });
  const [name, setName] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const formId = useId();
  const inputId = useId();
  const hintId = useId();
  const errorId = useId();
  const retire = change === "retire";
  const mismatch = hasCode(action.error, "invalid_input");
  const dismiss = () => {
    action.reset();
    setName("");
    onClose();
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (name !== environment.name || action.isPending) return;
    const body = { confirmName: name };
    action.run(async () => {
      try {
        const next = await result(
          retire
            ? client.POST(`${root}/retire`, { params: { path }, body })
            : client.POST(`${root}/restore`, { params: { path }, body }),
        );
        publishEnvironment(queries, path, next);
        onChanged(next);
        dismiss();
      } catch (error) {
        if (hasCode(error, "invalid_input")) input.current?.focus();
        // A refusal or a name mismatch can come from a change made elsewhere, such as another
        // owner's retirement or rename, so the page behind the dialog catches up with it. The
        // typed name stays for another try.
        if (hasCode(error, "conflict") || hasCode(error, "invalid_input"))
          void queries.invalidateQueries({
            queryKey: environmentQueries(path),
          });
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
          {retire ? (
            <Trans
              t={t}
              i18nKey={($) => $.lifecycle.retireDialogTitle}
              components={nameComponents}
            />
          ) : (
            <Trans
              t={t}
              i18nKey={($) => $.lifecycle.restoreDialogTitle}
              components={nameComponents}
            />
          )}
        </AlertDialogTitle>
        {/* The list is part of the description, so it is read with the dialog. */}
        <AlertDialogDescription
          render={<div />}
          className={e.dialogDescription}
        >
          {retire ? (
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
          ) : (
            <>
              <p>{t(($) => $.lifecycle.restoreDialogDescription)}</p>
              <ul className={e.effects}>
                <li>{t(($) => $.lifecycle.restoreEffects.nothingDeployed)}</li>
                <li>{t(($) => $.lifecycle.restoreEffects.staysOff)}</li>
                <li>{t(($) => $.lifecycle.restoreEffects.volumes)}</li>
              </ul>
            </>
          )}
        </AlertDialogDescription>
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
            variant={retire ? "danger" : "primary"}
            disabled={name !== environment.name || action.isPending}
          >
            {action.isPending
              ? retire
                ? t(($) => $.lifecycle.retiring)
                : t(($) => $.lifecycle.restoring)
              : retire
                ? t(($) => $.lifecycle.retire)
                : t(($) => $.lifecycle.restore)}
          </Button>
        </AlertDialogActions>
      </AlertDialogContent>
    </AlertDialogRoot>
  );
}
