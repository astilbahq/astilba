import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type RefObject, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ApiError,
  client,
  result,
  type Schema,
  setSession,
} from "../../api/client";
import { sessionOptions } from "../../api/session";
import { useSessionRequest } from "../../api/use-session-request";
import {
  AlertDialogActions,
  AlertDialogClose,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogRoot,
  AlertDialogTitle,
} from "../../components/alert-dialog";
import { Button } from "../../components/button";
import { LinkButton } from "../../components/link";
import { PasswordInput } from "../../components/password-input";
import { usePageTitle } from "../../i18n/use-page-title";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import { AuthenticatorSetup } from "../auth/authenticator-setup";
import { EmailVerification } from "../auth/email-verification";
import { IdentityConfirmation } from "../auth/identity-confirmation";
import { RecoveryCodesConfirmation } from "../auth/recovery-codes-confirmation";
import { useAuthValidation } from "../auth/use-auth-validation";
import * as styles from "./account.styles";

function needsRecentIdentity(recentAuth: boolean | undefined, error: unknown) {
  return (
    !recentAuth ||
    (error instanceof ApiError && error.detail.code === "recent_auth_required")
  );
}

function PasswordSection() {
  const { t } = useTranslation("account");
  const queries = useQueryClient();
  const session = useQuery(sessionOptions).data;
  const request = useSessionRequest();
  const action = useAction();
  const validation = useAuthValidation();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [saved, setSaved] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const focusHeading = () => heading.current?.focus();
  useEffect(() => {
    if (editing) heading.current?.focus();
  }, [editing]);
  return (
    <section className={styles.section} aria-labelledby="account-password">
      <div className={styles.intro}>
        <h2 id="account-password" ref={heading} tabIndex={-1}>
          {t(($) => $.password.title)}
        </h2>
        <p className={s.muted}>{t(($) => $.password.description)}</p>
      </div>
      {saved && <p role="status">{t(($) => $.password.saved)}</p>}
      {!editing ? (
        <div className={styles.actions}>
          <Button
            onClick={() => {
              setSaved(false);
              setEditing(true);
            }}
          >
            {t(($) => $.password.change)}
          </Button>
        </div>
      ) : (
        <div className={styles.task}>
          {needsRecentIdentity(session?.recentAuth, action.error) ? (
            <IdentityConfirmation
              onConfirmed={() => {
                action.reset();
                focusHeading();
              }}
            />
          ) : (
            <Form
              action={action}
              label={t(($) => $.password.change)}
              validation={validation.changePassword}
              onSubmit={(data) => {
                const password = text(data, "password");
                setDraft(password);
                action.run(() =>
                  request(
                    (signal) =>
                      result(
                        client.POST("/api/v1/auth/password", {
                          signal,
                          body: { password },
                        }),
                      ),
                    (next) => {
                      queries.setQueryData(
                        sessionOptions.queryKey,
                        setSession(next),
                      );
                      setDraft("");
                      setEditing(false);
                      setSaved(true);
                      focusHeading();
                    },
                  ),
                );
              }}
            >
              <Field
                name="password"
                label={t(($) => $.password.newPassword)}
                type="password"
                inputComponent={PasswordInput}
                autoComplete="new-password"
                defaultValue={draft}
                onChange={(event) => setDraft(event.currentTarget.value)}
              />
              <BusyButton busy={action.isPending}>
                {t(($) => $.password.save)}
              </BusyButton>
            </Form>
          )}
          <LinkButton
            disabled={action.isPending}
            onClick={() => {
              action.reset();
              setDraft("");
              setEditing(false);
              focusHeading();
            }}
          >
            {t(($) => $.cancel)}
          </LinkButton>
        </div>
      )}
    </section>
  );
}

type CredentialTask = "replace" | "disable";
function CredentialDialog({
  task,
  close,
  onCodes,
  onDisabled,
  returnFocus,
  fallbackFocus,
}: {
  task: CredentialTask | null;
  close: () => void;
  onCodes: (codes: Schema["RecoveryCodes"]) => void;
  onDisabled: () => void;
  returnFocus: RefObject<HTMLElement | null>;
  fallbackFocus: RefObject<HTMLHeadingElement | null>;
}) {
  const { t } = useTranslation("account");
  const session = useQuery(sessionOptions).data;
  const queries = useQueryClient();
  const action = useAction();
  const request = useSessionRequest();
  const validation = useAuthValidation();
  const cancel = useRef<HTMLButtonElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const identity = needsRecentIdentity(session?.recentAuth, action.error);
  const dismiss = () => {
    action.reset();
    close();
  };
  const confirm = () =>
    action.run(() =>
      task === "disable"
        ? request(
            (signal) =>
              result(client.POST("/api/v1/auth/mfa/disable", { signal })),
            (next) => {
              queries.setQueryData(sessionOptions.queryKey, setSession(next));
              onDisabled();
              dismiss();
            },
          )
        : request(
            (signal) =>
              result(
                client.POST("/api/v1/auth/mfa/recovery-codes", { signal }),
              ),
            (next) => {
              queries.setQueryData(
                sessionOptions.queryKey,
                setSession(next.session),
              );
              onCodes(next);
              dismiss();
            },
          ),
    );
  return (
    <AlertDialogRoot
      open={task !== null}
      onOpenChange={(open) => {
        if (!open && !action.isPending) dismiss();
      }}
    >
      <AlertDialogContent
        initialFocus={cancel}
        finalFocus={() =>
          returnFocus.current?.isConnected
            ? returnFocus.current
            : fallbackFocus.current
        }
      >
        <AlertDialogTitle ref={title} tabIndex={-1}>
          {task === "disable"
            ? t(($) => $.mfa.disableTitle)
            : t(($) => $.mfa.replaceTitle)}
        </AlertDialogTitle>
        <AlertDialogDescription>
          {task === "disable"
            ? t(($) => $.mfa.disableDescription)
            : t(($) => $.mfa.replaceDescription)}
        </AlertDialogDescription>
        {identity ? (
          <IdentityConfirmation
            onConfirmed={() => {
              action.reset();
              title.current?.focus();
            }}
          />
        ) : action.error ? (
          <p className={s.error} role="alert">
            {validation.setup.message(action.error)}
          </p>
        ) : null}
        <AlertDialogActions>
          <AlertDialogClose
            render={
              <Button ref={cancel} disabled={action.isPending}>
                {t(($) => $.cancel)}
              </Button>
            }
          />
          {!identity && (
            <Button
              variant={task === "disable" ? "danger" : "primary"}
              disabled={action.isPending}
              onClick={confirm}
            >
              {action.isPending
                ? task === "disable"
                  ? t(($) => $.mfa.disabling)
                  : t(($) => $.mfa.replacing)
                : task === "disable"
                  ? t(($) => $.mfa.disableConfirm)
                  : t(($) => $.mfa.replaceConfirm)}
            </Button>
          )}
        </AlertDialogActions>
      </AlertDialogContent>
    </AlertDialogRoot>
  );
}

function MFASection({ user }: { user: Schema["Account"] }) {
  const { t } = useTranslation("account");
  const [codes, setCodes] = useState<Schema["RecoveryCodes"]>();
  const [task, setTask] = useState<CredentialTask | null>(null);
  const [message, setMessage] = useState<"saved" | "disabledStatus">();
  const heading = useRef<HTMLHeadingElement>(null);
  const trigger = useRef<HTMLElement>(null);
  const focusHeading = () => heading.current?.focus();
  useEffect(() => {
    if (!user.mfaEnabled) setCodes(undefined);
  }, [user.mfaEnabled]);
  return (
    <section className={styles.section} aria-labelledby="account-mfa">
      <div className={styles.intro}>
        <h2 id="account-mfa" ref={heading} tabIndex={-1}>
          {t(($) => $.mfa.title)}
        </h2>
        <p className={s.muted}>
          {user.mfaEnabled ? t(($) => $.mfa.enabled) : t(($) => $.mfa.disabled)}
        </p>
        {user.mfaEnabled && (
          <p className={s.muted}>
            {t(($) => $.mfa.remaining, { count: user.recoveryCodesRemaining })}
          </p>
        )}
      </div>
      {message && (
        <p role="status">
          {message === "saved"
            ? t(($) => $.mfa.saved)
            : t(($) => $.mfa.disabledStatus)}
        </p>
      )}
      {user.mfaEnabled ? (
        codes ? (
          <div className={styles.task}>
            <RecoveryCodesConfirmation
              key={codes.generation}
              data={codes}
              replacing={false}
              onSaved={() => {
                setCodes(undefined);
                setMessage("saved");
                focusHeading();
              }}
              onReplace={() => {
                setCodes(undefined);
                setTask("replace");
              }}
            />
          </div>
        ) : (
          <>
            {!user.recoveryCodesSaved && (
              <p className={s.muted}>{t(($) => $.mfa.unsaved)}</p>
            )}
            <div className={styles.actions}>
              <Button
                onClick={(event) => {
                  trigger.current = event.currentTarget;
                  setMessage(undefined);
                  setTask("replace");
                }}
              >
                {t(($) => $.mfa.replace)}
              </Button>
              <Button
                variant="ghost"
                onClick={(event) => {
                  trigger.current = event.currentTarget;
                  setMessage(undefined);
                  setTask("disable");
                }}
              >
                {t(($) => $.mfa.disable)}
              </Button>
            </div>
          </>
        )
      ) : (
        <div className={styles.task}>
          <AuthenticatorSetup
            deferIdentity
            onEnrolled={(next) => {
              setCodes(next);
              setMessage(undefined);
              focusHeading();
            }}
            onResume={focusHeading}
          />
        </div>
      )}
      <CredentialDialog
        task={task}
        close={() => setTask(null)}
        onCodes={(next) => {
          setCodes(next);
          setMessage(undefined);
        }}
        onDisabled={() => {
          setCodes(undefined);
          setMessage("disabledStatus");
        }}
        returnFocus={trigger}
        fallbackFocus={heading}
      />
    </section>
  );
}

function AccountContent({ user }: { user: Schema["Account"] }) {
  const { t } = useTranslation("account");
  usePageTitle(t(($) => $.title));
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1>{t(($) => $.title)}</h1>
        <p className={s.muted}>
          <bdi>{user.email}</bdi>
        </p>
      </header>
      <section className={styles.section}>
        <EmailVerification embedded />
      </section>
      <PasswordSection />
      <MFASection user={user} />
    </div>
  );
}

export function Account() {
  const session = useQuery(sessionOptions).data;
  if (!session?.user || session.mfaPending) return null;
  return <AccountContent key={session.user.id} user={session.user} />;
}
