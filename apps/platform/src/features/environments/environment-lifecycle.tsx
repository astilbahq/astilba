import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useId, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { ApiError, client, result, type Schema } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
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

// Everything cached about the environment may have changed: retirement revokes its credentials
// and turns off its policies, and every write is now refused.
function publishEnvironment(
  queries: QueryClient,
  path: EnvironmentPath,
  next: Schema["Environment"],
) {
  const list = ["environments", path.workspaceId, path.projectId];
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
  const common = useTranslation("common").t;
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
        throw error;
      }
    });
  };
  const message = (error: unknown) =>
    !(error instanceof ApiError)
      ? common(($) => $.unexpectedError)
      : error.detail.code === "invalid_input"
        ? t(($) => $.lifecycle.nameMismatch)
        : error.detail.code === "forbidden"
          ? t(($) => $.lifecycle.forbidden)
          : // A conflict names the operation, upgrade or state in the way.
            error.message;
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
        <AlertDialogDescription>
          {retire
            ? t(($) => $.lifecycle.retireDialogDescription)
            : t(($) => $.lifecycle.restoreDialogDescription)}
        </AlertDialogDescription>
        {retire ? (
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
        ) : (
          <ul className={e.effects}>
            <li>{t(($) => $.lifecycle.restoreEffects.nothingDeployed)}</li>
            <li>{t(($) => $.lifecycle.restoreEffects.staysOff)}</li>
            <li>{t(($) => $.lifecycle.restoreEffects.volumes)}</li>
          </ul>
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
