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
// environment can retire a secret, or one of its earlier versions, that nothing still uses,
// except in a managed preview, which the server refuses: its template's policy controls it.
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
    target: Retirement;
    open: boolean;
  }>();
  const heading = useRef<HTMLHeadingElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const secrets = useQuery({
    queryKey: secretsKey(path),
    queryFn: () => result(client.GET(`${root}/secrets`, { params: { path } })),
  });
  // Versions arrive newest first within each secret, so a secret's first is its current one.
  const grouped = new Map<string, Schema["SecretVersion"][]>();
  for (const v of secrets.data ?? []) {
    const versions = grouped.get(v.secretId);
    if (versions) versions.push(v);
    else grouped.set(v.secretId, [v]);
  }
  const latest = [...grouped.values()].map(([current]) => current);
  const retire = (
    event: { currentTarget: HTMLButtonElement },
    target: Retirement,
  ) => {
    trigger.current = event.currentTarget;
    setMessage("");
    setRetiring({ target, open: true });
  };
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
        {latest.map((v) => {
          const earlier = grouped.get(v.secretId)?.slice(1) ?? [];
          return (
            <div key={v.id} className={e.secretEntry}>
              <div className={s.spread}>
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
                    aria-label={t(($) => $.secrets.retireNamed, {
                      name: v.name,
                    })}
                    onClick={(event) =>
                      retire(event, { kind: "secret", version: v })
                    }
                  >
                    {t(($) => $.secrets.retire)}
                  </Button>
                )}
              </div>
              {earlier.length > 0 && (
                <details className={e.earlierVersions}>
                  <summary>
                    {t(($) => $.secrets.earlierVersions, {
                      count: earlier.length,
                    })}
                  </summary>
                  <ul className={s.list}>
                    {earlier.map((version) => (
                      <li key={version.id} className={s.spread}>
                        <div>
                          <p className={s.code}>{version.id}</p>
                          <small>
                            {t(($) => $.secrets.created, {
                              date: new Date(version.createdAt).toLocaleString(
                                i18n.resolvedLanguage,
                              ),
                            })}
                          </small>
                        </div>
                        {retirable && (
                          <Button
                            size="sm"
                            aria-label={t(($) => $.secrets.retireVersionNamed, {
                              name: v.name,
                              id: version.id,
                            })}
                            onClick={(event) =>
                              retire(event, { kind: "version", version })
                            }
                          >
                            {t(($) => $.secrets.retireVersion)}
                          </Button>
                        )}
                      </li>
                    ))}
                  </ul>
                </details>
              )}
            </div>
          );
        })}
      </div>
      {write && (
        <Form
          action={action}
          onSubmit={(d, form) =>
            action.run(async () => {
              const name = text(d, "name");
              const existing = latest.find((v) => v.name === name);
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
          target={retiring.target}
          path={path}
          onClose={() =>
            setRetiring((current) => current && { ...current, open: false })
          }
          onRetired={({ kind, version }) =>
            setMessage(
              kind === "secret"
                ? t(($) => $.secrets.retired, { name: version.name })
                : t(($) => $.secrets.versionRetired, {
                    name: version.name,
                    id: version.id,
                  }),
            )
          }
          finalFocus={() =>
            trigger.current?.isConnected ? trigger.current : heading.current
          }
        />
      )}
    </section>
  );
}

// What an owner retires: a whole secret, named by its current version, or one earlier version.
type Retirement = {
  kind: "secret" | "version";
  version: Schema["SecretVersion"];
};

// Codes and refusal reasons select the copy, never the server's English.
function useRetireErrorText() {
  const { t } = useTranslation("environment");
  const common = useTranslation("common").t;
  return (error: unknown, kind: Retirement["kind"]) => {
    if (!(error instanceof ApiError)) return common(($) => $.unexpectedError);
    const fields = error.detail.fields ?? [];
    switch (error.detail.code) {
      case "conflict":
        return fields.includes("secret.inUse")
          ? t(($) => $.secrets.refusal.inUse)
          : fields.includes("secretVersion.inUse")
            ? t(($) => $.secrets.refusal.versionInUse)
            : fields.includes("secretVersion.current")
              ? t(($) => $.secrets.refusal.current)
              : fields.includes("environment.state")
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
        return kind === "secret"
          ? t(($) => $.secrets.refusal.unavailable)
          : t(($) => $.secrets.refusal.versionUnavailable);
      default:
        return common(($) => $.requestFailed);
    }
  };
}

// Retiring a secret erases every version's value, and retiring a version erases its own; neither
// can be undone, so both need a recent identity confirmation, which the shared step-up dialog
// asks for over this one.
function RetireSecretDialog({
  open,
  target,
  path,
  onClose,
  onRetired,
  finalFocus,
}: {
  open: boolean;
  target: Retirement;
  path: EnvironmentPath;
  onClose: () => void;
  onRetired: (target: Retirement) => void;
  finalFocus: () => HTMLElement | null;
}) {
  const { kind, version } = target;
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
          kind === "secret"
            ? client.DELETE(`${root}/secrets/{secretId}`, {
                params: { path: { ...path, secretId: version.secretId } },
              })
            : client.DELETE(`${root}/secrets/{secretId}/versions/{versionId}`, {
                params: {
                  path: {
                    ...path,
                    secretId: version.secretId,
                    versionId: version.id,
                  },
                },
              }),
        );
        // The list drops what was retired before the dialog closes, so focus moves on from its row.
        await queries.invalidateQueries({ queryKey: secretsKey(path) });
        onRetired(target);
        dismiss();
      } catch (error) {
        if (error instanceof ApiError) {
          // A secret or version retired elsewhere, or a version that became current, shows as it
          // now is; an environment retired elsewhere is read-only, which the page behind the
          // dialog then shows.
          if (
            error.detail.code === "not_found" ||
            error.detail.fields?.includes("secretVersion.current")
          )
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
          {kind === "secret" ? (
            <Trans
              t={t}
              i18nKey={($) => $.secrets.retireDialogTitle}
              components={{ name: <bdi>{version.name}</bdi> }}
            />
          ) : (
            <Trans
              t={t}
              i18nKey={($) => $.secrets.retireVersionDialogTitle}
              components={{ name: <bdi>{version.name}</bdi> }}
            />
          )}
        </AlertDialogTitle>
        {/* The list is part of the description, so it is read with the dialog. */}
        <AlertDialogDescription
          render={<div />}
          className={e.dialogDescription}
        >
          {kind === "secret" ? (
            <>
              <p>{t(($) => $.secrets.retireDialogDescription)}</p>
              <ul className={e.effects}>
                <li>{t(($) => $.secrets.retireEffects.values)}</li>
                <li>{t(($) => $.secrets.retireEffects.name)}</li>
                <li>{t(($) => $.secrets.retireEffects.revisions)}</li>
              </ul>
              <p>{t(($) => $.secrets.retireDialogInUse)}</p>
            </>
          ) : (
            <>
              <p>
                {t(($) => $.secrets.retireVersionDialogDescription, {
                  id: version.id,
                })}
              </p>
              <ul className={e.effects}>
                <li>{t(($) => $.secrets.retireVersionEffects.value)}</li>
                <li>{t(($) => $.secrets.retireVersionEffects.current)}</li>
                <li>{t(($) => $.secrets.retireVersionEffects.revisions)}</li>
              </ul>
              <p>{t(($) => $.secrets.retireVersionDialogInUse)}</p>
            </>
          )}
        </AlertDialogDescription>
        {action.error && (
          <p id={errorId} className={s.error} role="alert">
            {message(action.error, kind)}
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
              : kind === "secret"
                ? t(($) => $.secrets.retire)
                : t(($) => $.secrets.retireVersion)}
          </Button>
        </AlertDialogActions>
      </AlertDialogContent>
    </AlertDialogRoot>
  );
}
