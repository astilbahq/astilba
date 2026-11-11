import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import { ApiError, client, result, setSession } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { type StepUp, StepUpContext } from "../../api/step-up";
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
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import { AuthenticatorInput } from "./authenticator-input";
import * as styles from "./step-up.styles";
import { useAuthValidation } from "./use-auth-validation";
import type { StepUpCredential } from "./validation";

type Waiter = { resolve: (confirmed: boolean) => void; withdraw: () => void };

/**
 * Offers identity confirmation to every action below it, in one dialog shared by all of
 * them. It can open over another dialog: the action that opened it is still pending, which
 * keeps that dialog open until this one closes and returns focus to it.
 */
export function StepUpProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  // Each prompt gets a fresh dialog, even one opened while the last is still closing.
  const [prompt, setPrompt] = useState(0);
  const waiting = useRef<Waiter[]>([]);
  const returnTo = useRef<Element | null>(null);
  const settle = useCallback((confirmed: boolean) => {
    const waiters = waiting.current;
    waiting.current = [];
    setOpen(false);
    for (const waiter of waiters) {
      waiter.withdraw();
      waiter.resolve(confirmed);
    }
  }, []);
  const stepUp = useCallback<StepUp>(
    ({ origin, signal } = {}) =>
      new Promise((resolve) => {
        if (signal?.aborted) return resolve(false);
        if (waiting.current.length === 0) {
          returnTo.current = origin ?? document.activeElement;
          setPrompt((current) => current + 1);
        }
        const leave = () => {
          waiting.current = waiting.current.filter((w) => w !== waiter);
          if (waiting.current.length === 0) setOpen(false);
          resolve(false);
        };
        const waiter: Waiter = {
          resolve,
          withdraw: () => signal?.removeEventListener("abort", leave),
        };
        signal?.addEventListener("abort", leave, { once: true });
        waiting.current.push(waiter);
        setOpen(true);
      }),
    [],
  );
  // Only an application teardown removes the provider; nothing is left waiting on it.
  useEffect(() => () => settle(false), [settle]);
  return (
    <>
      <StepUpContext value={stepUp}>{children}</StepUpContext>
      <StepUpDialog
        prompt={prompt}
        open={open}
        onSettled={settle}
        onClosed={() => restoreFocus(returnTo.current)}
      />
    </>
  );
}

// Focus that is nowhere, or still in a dialog that is closing, is free to move.
function focusIsFree(document: Document) {
  const active = document.activeElement;
  return (
    !active ||
    active === document.body ||
    !active.isConnected ||
    active.closest("[role=alertdialog][data-closed]") !== null
  );
}

/**
 * Returns focus to the control that asked, once the dialog has closed. That control is
 * usually disabled while its action waits, and focus cannot land on a disabled control, so
 * it moves there once the control is enabled again, unless the person has moved focus
 * somewhere else meanwhile.
 */
function restoreFocus(target: Element | null) {
  if (
    !(target instanceof HTMLElement) ||
    !target.isConnected ||
    target === target.ownerDocument.body
  )
    return;
  const document = target.ownerDocument;
  if (!target.matches(":disabled")) {
    if (focusIsFree(document)) target.focus();
    return;
  }
  const moved = () => {
    if (!focusIsFree(document)) stop();
  };
  const stop = () => {
    observer.disconnect();
    document.removeEventListener("focusin", moved);
    clearTimeout(timeout);
  };
  const observer = new MutationObserver(() => {
    if (!target.isConnected) return stop();
    if (target.matches(":disabled")) return;
    stop();
    if (focusIsFree(document)) target.focus();
  });
  observer.observe(target, { attributes: true, attributeFilter: ["disabled"] });
  document.addEventListener("focusin", moved);
  const timeout = setTimeout(stop, 60_000);
}

/**
 * The dialog stays mounted across prompts, so each one opens with the same transition. Its
 * contents are keyed by prompt, so a prompt opened while the last is still closing starts
 * without the last one's error, mode or typed credential.
 */
function StepUpDialog({
  prompt,
  open,
  onSettled,
  onClosed,
}: {
  prompt: number;
  open: boolean;
  onSettled: (confirmed: boolean) => void;
  onClosed: () => void;
}) {
  // A confirmation in flight keeps the dialog open; its outcome closes it.
  const pending = useRef(false);
  return (
    <AlertDialogRoot
      open={open}
      onOpenChange={(next) => {
        if (!next && !pending.current) onSettled(false);
      }}
      onOpenChangeComplete={(next) => {
        if (!next) onClosed();
      }}
    >
      {/* The provider returns focus itself, once the control that asked can take it. */}
      <AlertDialogContent finalFocus={false}>
        <StepUpPrompt
          key={prompt}
          open={open}
          onSettled={onSettled}
          onPendingChange={(value) => {
            pending.current = value;
          }}
        />
      </AlertDialogContent>
    </AlertDialogRoot>
  );
}

function StepUpPrompt({
  open,
  onSettled,
  onPendingChange,
}: {
  open: boolean;
  onSettled: (confirmed: boolean) => void;
  onPendingChange: (pending: boolean) => void;
}) {
  const { t } = useTranslation("auth");
  const queries = useQueryClient();
  const user = useQuery(sessionOptions).data?.user;
  const action = useAction();
  const request = useSessionRequest({ renewal: true });
  const validation = useAuthValidation();
  const [recovery, setRecovery] = useState(false);
  const fields = useRef<HTMLDivElement>(null);
  const credential: StepUpCredential = !user?.mfaEnabled
    ? "password"
    : recovery
      ? "recovery"
      : "authenticator";
  const signedOut =
    action.error instanceof ApiError && action.error.status === 401;
  const account = user?.id;
  const openedFor = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!open) {
      openedFor.current = undefined;
      return;
    }
    openedFor.current ??= account;
    // The session ended, or another account signed in, so the waiting actions are no
    // longer this person's to confirm. Signing in again is the only way on.
    if (signedOut || !account || account !== openedFor.current)
      onSettled(false);
  }, [open, account, signedOut, onSettled]);
  useLayoutEffect(() => {
    onPendingChange(action.isPending);
  }, [onPendingChange, action.isPending]);
  const switched = useRef(false);
  useEffect(() => {
    // The field remounts for the other kind of code; keep focus in it.
    if (!switched.current) return;
    switched.current = false;
    fields.current?.querySelector("input")?.focus();
  }, [recovery]);
  const switchCredential = () => {
    action.reset();
    switched.current = true;
    setRecovery(!recovery);
  };
  return (
    <>
      <AlertDialogTitle>{t(($) => $.stepUp.title)}</AlertDialogTitle>
      <AlertDialogDescription>
        {credential === "password"
          ? t(($) => $.stepUp.passwordDescription)
          : credential === "recovery"
            ? t(($) => $.stepUp.recoveryDescription)
            : t(($) => $.stepUp.authenticatorDescription)}
      </AlertDialogDescription>
      <Form
        key={credential}
        action={action}
        label={t(($) => $.stepUp.title)}
        validation={validation.stepUp(credential)}
        onSubmit={(data) =>
          action.run(() =>
            request(
              (signal) =>
                result(
                  client.POST("/api/v1/auth/reauthenticate", {
                    signal,
                    body:
                      credential === "password"
                        ? { password: text(data, "password") }
                        : { code: text(data, "code").trim() },
                  }),
                ),
              (next) => {
                queries.setQueryData(sessionOptions.queryKey, setSession(next));
                onSettled(true);
              },
            ),
          )
        }
      >
        <div ref={fields} className={styles.credential}>
          {credential === "password" ? (
            <Field
              name="password"
              type="password"
              autoComplete="current-password"
              inputComponent={PasswordInput}
              label={t(($) => $.stepUp.password)}
            />
          ) : credential === "recovery" ? (
            <Field
              name="code"
              label={t(($) => $.stepUp.recoveryCode)}
              autoComplete="one-time-code"
              autoCapitalize="characters"
              spellCheck={false}
              description={t(($) => $.mfa.recoveryWarning)}
            />
          ) : (
            <Field
              name="code"
              label={t(($) => $.stepUp.code)}
              inputComponent={AuthenticatorInput}
              autoComplete="one-time-code"
            />
          )}
          {credential !== "password" && (
            <div>
              <LinkButton
                disabled={action.isPending}
                onClick={switchCredential}
              >
                {recovery
                  ? t(($) => $.stepUp.useAuthenticator)
                  : t(($) => $.stepUp.useRecovery)}
              </LinkButton>
            </div>
          )}
        </div>
        <AlertDialogActions>
          <AlertDialogClose
            render={
              <Button disabled={action.isPending}>
                {t(($) => $.stepUp.cancel)}
              </Button>
            }
          />
          <BusyButton busy={action.isPending}>
            {t(($) => $.stepUp.confirm)}
          </BusyButton>
        </AlertDialogActions>
      </Form>
    </>
  );
}
