import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState } from "react";
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
import * as s from "../../styles";
import {
  BusyButton,
  ErrorNotice,
  Field,
  Form,
  text,
  useAction,
} from "../../ui";
import * as e from "./environment.styles";
import type { EnvProps } from "./environment.types";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}";

const secretsKey = (path: EnvironmentPath) => ["secrets", path];

// Secret values are write-only. Members with write access add versions; owners of an active
// environment can retire a secret that nothing still uses, except in a managed preview, which
// the server refuses: its template's policy controls it.
export function Secrets({
  workspaceId,
  projectId,
  environment,
  write,
  owner,
}: EnvProps) {
  const { t, i18n } = useTranslation("environment");
  const path = { workspaceId, projectId, environmentId: environment.id };
  const retirable = owner && environment.purpose !== "preview";
  const queries = useQueryClient();
  const action = useAction();
  const [message, setMessage] = useState("");
  const [retiring, setRetiring] = useState<{
    secret: Schema["SecretVersion"];
    open: boolean;
  }>();
  const heading = useRef<HTMLHeadingElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const secrets = useQuery({
    queryKey: secretsKey(path),
    queryFn: () => result(client.GET(`${root}/secrets`, { params: { path } })),
  });
  const latest = secrets.data?.filter(
    (v, i, all) => all.findIndex((x) => x.secretId === v.secretId) === i,
  );
  return (
    <section className={s.panel}>
      <h2 ref={heading} tabIndex={-1} className={e.recoveryTarget}>
        {t(($) => $.secrets.title)}
      </h2>
      <p className={s.muted}>
        <Trans
          t={t}
          i18nKey={($) => $.secrets.description}
          components={{ field: <code>secretVersionId</code> }}
        />
      </p>
      <ErrorNotice error={action.error ?? secrets.error} />
      {/* Always mounted, so a message is announced when it appears. */}
      <div role="status">
        {message && <p className={s.notice}>{message}</p>}
      </div>
      <div className={s.list}>
        {latest?.map((v) => (
          <div key={v.id} className={s.spread}>
            <div>
              <strong>
                <bdi>{v.name}</bdi>
              </strong>
              <p className={s.code}>{v.id}</p>
              <small>
                {t(($) => $.secrets.created, {
                  date: new Date(v.createdAt).toLocaleString(
                    i18n.resolvedLanguage,
                  ),
                })}
              </small>
            </div>
            {retirable && (
              <Button
                size="sm"
                aria-label={t(($) => $.secrets.retireNamed, { name: v.name })}
                onClick={(event) => {
                  trigger.current = event.currentTarget;
                  setMessage("");
                  setRetiring({ secret: v, open: true });
                }}
              >
                {t(($) => $.secrets.retire)}
              </Button>
            )}
          </div>
        ))}
      </div>
      {write && (
        <Form
          action={action}
          onSubmit={(d, form) =>
            action.run(async () => {
              const name = text(d, "name");
              const existing = latest?.find((v) => v.name === name);
              const value = text(d, "value");
              if (existing)
                await result(
                  client.POST(`${root}/secrets/{secretId}/versions`, {
                    params: { path: { ...path, secretId: existing.secretId } },
                    body: { value },
                  }),
                );
              else
                await result(
                  client.POST(`${root}/secrets`, {
                    params: { path },
                    body: { name, value },
                  }),
                );
              form.reset();
              setMessage(t(($) => $.secrets.saved));
              await queries.cancelQueries({ queryKey: secretsKey(path) });
              await queries.invalidateQueries({ queryKey: secretsKey(path) });
            })
          }
        >
          <Field name="name" label={t(($) => $.secrets.name)} />
          <Field
            name="value"
            label={t(($) => $.secrets.value)}
            type="password"
            autoComplete="off"
          />
          <small>{t(($) => $.secrets.hint)}</small>
          <BusyButton busy={action.isPending}>
            {t(($) => $.secrets.save)}
          </BusyButton>
        </Form>
      )}
      {retiring && (
        <RetireSecretDialog
          open={retiring.open}
          secret={retiring.secret}
          path={path}
          onClose={() =>
            setRetiring((current) => current && { ...current, open: false })
          }
          onRetired={(name) =>
            setMessage(t(($) => $.secrets.retired, { name }))
          }
          finalFocus={() =>
            trigger.current?.isConnected ? trigger.current : heading.current
          }
        />
      )}
    </section>
  );
}

// Codes and refusal reasons select the copy, never the server's English.
function useRetireErrorText() {
  const { t } = useTranslation("environment");
  const common = useTranslation("common").t;
  return (error: unknown) => {
    if (!(error instanceof ApiError)) return common(($) => $.unexpectedError);
    switch (error.detail.code) {
      case "conflict":
        return error.detail.fields?.includes("secret.inUse")
          ? t(($) => $.secrets.refusal.inUse)
          : error.detail.fields?.includes("environment.state")
            ? t(($) => $.secrets.refusal.inactive)
            : t(($) => $.secrets.refusal.unknown);
      case "forbidden":
        return t(($) => $.secrets.refusal.forbidden);
      case "mfa_required":
        return t(($) => $.secrets.refusal.mfa);
      case "email_verification_required":
        return t(($) => $.secrets.refusal.verifyEmail);
      case "subscription_required":
        return t(($) => $.secrets.refusal.subscription);
      case "recent_auth_required":
        return t(($) => $.secrets.refusal.recentAuth);
      case "not_found":
        return t(($) => $.secrets.refusal.unavailable);
      default:
        return common(($) => $.requestFailed);
    }
  };
}

// Retiring a secret erases every version's value and cannot be undone, so it needs a recent
// identity confirmation, which the shared step-up dialog asks for over this one.
function RetireSecretDialog({
  open,
  secret,
  path,
  onClose,
  onRetired,
  finalFocus,
}: {
  open: boolean;
  secret: Schema["SecretVersion"];
  path: EnvironmentPath;
  onClose: () => void;
  onRetired: (name: string) => void;
  finalFocus: () => HTMLElement | null;
}) {
  const { t } = useTranslation("environment");
  const message = useRetireErrorText();
  const queries = useQueryClient();
  const action = useAction({ recentAuth: true });
  const errorId = useId();
  const dismiss = () => {
    action.reset();
    onClose();
  };
  const retire = () =>
    action.run(async () => {
      try {
        await result(
          client.DELETE(`${root}/secrets/{secretId}`, {
            params: { path: { ...path, secretId: secret.secretId } },
          }),
        );
        // The list drops the secret before the dialog closes, so focus moves on from its row.
        await queries.invalidateQueries({ queryKey: secretsKey(path) });
        onRetired(secret.name);
        dismiss();
      } catch (error) {
        if (error instanceof ApiError) {
          // A secret retired elsewhere leaves the list; an environment retired elsewhere is
          // read-only, which the page behind the dialog then shows.
          if (error.detail.code === "not_found")
            void queries.invalidateQueries({ queryKey: secretsKey(path) });
          if (error.detail.fields?.includes("environment.state"))
            void queries.invalidateQueries({
              queryKey: environmentsOptions(path.workspaceId, path.projectId)
                .queryKey,
            });
        }
        throw error;
      }
    });
  return (
    <AlertDialogRoot
      open={open}
      onOpenChange={(next) => {
        if (!next && !action.isPending) dismiss();
      }}
    >
      <AlertDialogContent finalFocus={finalFocus}>
        <AlertDialogTitle className={e.dialogTitle}>
          <Trans
            t={t}
            i18nKey={($) => $.secrets.retireDialogTitle}
            components={{ name: <bdi>{secret.name}</bdi> }}
          />
        </AlertDialogTitle>
        {/* The list is part of the description, so it is read with the dialog. */}
        <AlertDialogDescription
          render={<div />}
          className={e.dialogDescription}
        >
          <p>{t(($) => $.secrets.retireDialogDescription)}</p>
          <ul className={e.effects}>
            <li>{t(($) => $.secrets.retireEffects.values)}</li>
            <li>{t(($) => $.secrets.retireEffects.name)}</li>
            <li>{t(($) => $.secrets.retireEffects.revisions)}</li>
          </ul>
          <p>{t(($) => $.secrets.retireDialogInUse)}</p>
        </AlertDialogDescription>
        {action.error && (
          <p id={errorId} className={s.error} role="alert">
            {message(action.error)}
          </p>
        )}
        <AlertDialogActions>
          <AlertDialogClose
            render={
              <Button disabled={action.isPending}>
                {t(($) => $.secrets.cancel)}
              </Button>
            }
          />
          <Button
            variant="danger"
            disabled={action.isPending}
            aria-describedby={action.error ? errorId : undefined}
            onClick={retire}
          >
            {action.isPending
              ? t(($) => $.secrets.retiring)
              : t(($) => $.secrets.retire)}
          </Button>
        </AlertDialogActions>
      </AlertDialogContent>
    </AlertDialogRoot>
  );
}
