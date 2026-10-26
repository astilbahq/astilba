import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { field } from "../../../styled-system/recipes";
import { ApiError, client, result, setSession } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { useSessionRequest } from "../../api/use-session-request";
import { Button } from "../../components/button";
import { Link } from "../../components/link";
import { ErrorToast } from "../../components/toast";
import { usePageTitle } from "../../i18n/use-page-title";
import * as s from "../../styles";
import { BusyButton, Form, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { useAuthLinkCheck } from "./use-auth-link-check";
import { useAuthValidation } from "./use-auth-validation";

export function EmailVerification({
  token,
  embedded = false,
}: {
  token?: string;
  embedded?: boolean;
}) {
  const { t } = useTranslation("auth");
  const session = useQuery(sessionOptions).data;
  const user = !session?.mfaPending ? session?.user : undefined;
  const validation = useAuthValidation();
  const request = useAction();
  const sessionRequest = useSessionRequest();
  const verify = useAction();
  const [sent, setSent] = useState(false);
  const [done, setDone] = useState(false);
  const link = useAuthLinkCheck(
    "email",
    user && !user.emailVerified && !done && !sent ? token : undefined,
    user?.id,
  );
  const step = !user
    ? "guest"
    : user.emailVerified || done
      ? "complete"
      : sent
        ? "sent"
        : !token
          ? "request"
          : link.state === "ready"
            ? "verify"
            : link.state;
  const heading = useRef<HTMLHeadingElement>(null);
  const previous = useRef(step);
  useEffect(() => {
    if (previous.current !== step) heading.current?.focus();
    previous.current = step;
  }, [step]);
  const Heading = embedded ? "h2" : "h1";
  return (
    <div className={auth.flow}>
      <div className={auth.headingGroup}>
        <Heading ref={heading} tabIndex={-1}>
          {step === "complete"
            ? t(($) => $.emailVerification.completeTitle)
            : step === "sent"
              ? t(($) => $.emailVerification.sentTitle)
              : step === "invalid"
                ? t(($) => $.emailVerification.invalidTitle)
                : step === "unavailable"
                  ? t(($) => $.emailVerification.unavailableTitle)
                  : t(($) => $.emailVerification.title)}
        </Heading>
        {step === "request" && (
          <p className={s.muted}>{t(($) => $.emailVerification.description)}</p>
        )}
        {step === "verify" && user && (
          <p className={s.muted}>
            <Trans
              t={t}
              i18nKey={($) => $.emailVerification.account}
              components={{ email: <bdi>{user.email}</bdi> }}
            />
          </p>
        )}
      </div>
      <div className={s.stack}>
        {step === "guest" && (
          <>
            <p className={s.muted}>{t(($) => $.emailVerification.guest)}</p>
            <Link
              to="/login"
              search={{
                returnTo: `/verify-email${token ? `?${new URLSearchParams({ token })}` : ""}`,
              }}
              className={auth.secondaryLink}
            >
              {t(($) => $.emailVerification.signIn)}
            </Link>
          </>
        )}
        {step === "checking" && (
          <p className={s.muted} role="status">
            {t(($) => $.emailVerification.checking)}
          </p>
        )}
        {step === "invalid" && (
          <p className={s.muted}>{t(($) => $.validation.invalidEmailLink)}</p>
        )}
        {step === "unavailable" && (
          <>
            <ErrorToast
              error={link.error}
              message={validation.emailVerification.message(link.error)}
            />
            <Button
              disabled={link.isFetching}
              onClick={() => void link.refetch()}
            >
              {t(($) => $.emailVerification.retry)}
            </Button>
          </>
        )}
        {step === "verify" && token && (
          <Form
            action={verify}
            validation={validation.emailVerification}
            onSubmit={() =>
              verify.run(() =>
                sessionRequest(
                  (signal) =>
                    result(
                      client.POST("/api/v1/auth/email/verify", {
                        signal,
                        body: { token },
                      }),
                    ),
                  (next) => {
                    setSession(next);
                    setDone(true);
                  },
                ),
              )
            }
          >
            {(error) => (
              <>
                {error && (
                  <p className={field().error} role="alert">
                    {error}
                  </p>
                )}
                <BusyButton busy={verify.isPending}>
                  {t(($) => $.emailVerification.verify)}
                </BusyButton>
              </>
            )}
          </Form>
        )}
        {step === "sent" && user && (
          <p className={s.muted} role="status">
            <Trans
              t={t}
              i18nKey={($) => $.emailVerification.sent}
              components={{ email: <bdi>{user.email}</bdi> }}
            />
          </p>
        )}
        {step === "complete" && (
          <p className={s.muted} role="status">
            {t(($) => $.emailVerification.complete)}
          </p>
        )}
        {(step === "request" ||
          step === "invalid" ||
          step === "sent" ||
          (step === "verify" &&
            verify.error instanceof ApiError &&
            verify.error.detail.code === "invalid_credentials")) && (
          <>
            <ErrorToast
              error={request.error}
              message={validation.emailVerification.message(request.error)}
            />
            <Button
              disabled={request.isPending}
              onClick={() =>
                request.run(() =>
                  sessionRequest(
                    (signal) =>
                      result(
                        client.POST("/api/v1/auth/email/request", { signal }),
                      ),
                    () => {
                      setSent(true);
                      if (step === "sent") heading.current?.focus();
                    },
                  ),
                )
              }
            >
              {request.isPending
                ? t(($) => $.emailVerification.sending)
                : step === "sent"
                  ? t(($) => $.emailVerification.resend)
                  : t(($) => $.emailVerification.send)}
            </Button>
          </>
        )}
        {!embedded && user && (
          <div className={auth.secondaryActions}>
            <Link to="/" className={auth.secondaryLink}>
              {t(($) => $.emailVerification.backToProjects)}
            </Link>
          </div>
        )}
      </div>
    </div>
  );
}

export function VerifyEmailPage({ token }: { token?: string }) {
  const { t } = useTranslation("auth");
  usePageTitle(t(($) => $.emailVerification.pageTitle));
  const session = useQuery(sessionOptions).data;
  return (
    <EmailVerification
      key={`${session?.user?.id ?? "guest"}:${token ?? "request"}`}
      token={token}
    />
  );
}
