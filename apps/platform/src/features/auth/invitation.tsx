import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { field } from "../../../styled-system/recipes";
import { client, result, setSession } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { Button } from "../../components/button";
import { Link } from "../../components/link";
import { ErrorToast } from "../../components/toast";
import { usePageTitle } from "../../i18n/use-page-title";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { useAuthLinkCheck } from "./use-auth-link-check";
import { useAuthValidation } from "./use-auth-validation";

export function Invitation({ token }: { token?: string }) {
  const { t } = useTranslation("auth");
  usePageTitle(t(($) => $.invitation.pageTitle));
  const authValidation = useAuthValidation();
  const action = useAction();
  const session = useQuery(sessionOptions).data;
  const [done, setDone] = useState(false);
  const link = useAuthLinkCheck("invitation", token);
  const step = !token
    ? "missing"
    : done
      ? "complete"
      : link.state === "ready"
        ? "accept"
        : link.state;
  const heading = useRef<HTMLHeadingElement>(null);
  const previousStep = useRef(step);
  const returnTo = token
    ? `/invitation?${new URLSearchParams({ token })}`
    : undefined;

  useEffect(() => {
    if (previousStep.current !== step) heading.current?.focus();
    previousStep.current = step;
  }, [step]);

  return (
    <div className={auth.flow}>
      <div className={auth.headingGroup}>
        <h1 ref={heading} tabIndex={-1}>
          {step === "complete"
            ? t(($) => $.invitation.completeTitle)
            : step === "invalid"
              ? t(($) => $.invitation.invalidLinkTitle)
              : step === "unavailable"
                ? t(($) => $.invitation.checkUnavailableTitle)
                : t(($) => $.invitation.title)}
        </h1>
        {step === "accept" && (
          <p className={s.muted}>
            {session?.user ? (
              <Trans
                t={t}
                i18nKey={($) => $.invitation.signedIn}
                components={{ email: <bdi>{session.user.email}</bdi> }}
              />
            ) : (
              t(($) => $.invitation.signInFirst)
            )}
          </p>
        )}
      </div>
      <section className={s.stack}>
        {step === "missing" ? (
          <p className={s.muted}>{t(($) => $.invitation.missingToken)}</p>
        ) : step === "checking" ? (
          <p className={s.muted} role="status">
            {t(($) => $.invitation.checkingLink)}
          </p>
        ) : step === "invalid" ? (
          <p className={s.muted}>{t(($) => $.validation.invalidInvitation)}</p>
        ) : step === "unavailable" ? (
          <>
            <ErrorToast
              error={link.error}
              message={authValidation.invitation.message(link.error)}
            />
            <Button
              disabled={link.isFetching}
              onClick={() => void link.refetch()}
            >
              {t(($) => $.invitation.retryLinkCheck)}
            </Button>
          </>
        ) : step === "complete" ? (
          <p className={s.muted} role="status">
            {t(($) => $.invitation.complete)}
          </p>
        ) : token ? (
          <>
            <Form
              action={action}
              validation={
                session?.user
                  ? authValidation.acceptInvitation
                  : authValidation.invitation
              }
              onSubmit={(data) =>
                action.run(async () => {
                  setSession(
                    await result(
                      client.POST("/api/v1/auth/invitations/accept", {
                        body: {
                          token,
                          name: text(data, "name") || undefined,
                          password: text(data, "password") || undefined,
                        },
                      }),
                    ),
                  );
                  setDone(true);
                })
              }
            >
              {(error) => (
                <>
                  {!session?.user && (
                    <Field
                      name="name"
                      label={t(($) => $.invitation.name)}
                      autoComplete="name"
                      placeholder={t(($) => $.invitation.namePlaceholder)}
                    />
                  )}
                  {(!session?.user || error) && (
                    <div className={auth.feedbackGroup}>
                      {!session?.user && (
                        <Field
                          name="password"
                          label={t(($) => $.invitation.password)}
                          type="password"
                          autoComplete="new-password"
                          placeholder={t(
                            ($) => $.invitation.passwordPlaceholder,
                          )}
                        />
                      )}
                      {error && (
                        <p className={field().error} role="alert">
                          {error}
                        </p>
                      )}
                    </div>
                  )}
                  <BusyButton busy={action.isPending}>
                    {t(($) => $.invitation.submit)}
                  </BusyButton>
                </>
              )}
            </Form>
          </>
        ) : null}
        <div className={auth.secondaryActions}>
          {done ? (
            <Link to="/" className={auth.secondaryLink}>
              {t(($) => $.invitation.openProjects)}
            </Link>
          ) : session?.user ? (
            <Link to="/" className={auth.secondaryLink}>
              {t(($) => $.invitation.backToProjects)}
            </Link>
          ) : (
            <Link
              to="/login"
              search={{ returnTo }}
              className={auth.secondaryLink}
            >
              {t(($) => $.backToSignIn)}
            </Link>
          )}
        </div>
      </section>
    </div>
  );
}
