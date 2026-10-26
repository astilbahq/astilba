import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { field } from "../../../styled-system/recipes";
import { client, result } from "../../api/client";
import { Button } from "../../components/button";
import { Link } from "../../components/link";
import { ErrorToast } from "../../components/toast";
import { usePageTitle } from "../../i18n/use-page-title";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { useAuthLinkCheck } from "./use-auth-link-check";
import { useAuthValidation } from "./use-auth-validation";

type Step =
  | "request"
  | "sent"
  | "checking"
  | "invalid"
  | "unavailable"
  | "password"
  | "complete";

export function PasswordReset({ token }: { token?: string }) {
  const { t } = useTranslation("auth");
  usePageTitle(t(($) => $.reset.pageTitle));
  const authValidation = useAuthValidation();
  const [step, setStep] = useState<"request" | "sent" | "complete">("request");
  const request = useAction();
  const completion = useAction();
  const link = useAuthLinkCheck("reset", token);
  const currentStep: Step =
    token && step !== "complete"
      ? link.state === "ready"
        ? "password"
        : link.state
      : step;
  const heading = useRef<HTMLHeadingElement>(null);
  const previousStep = useRef(currentStep);

  useEffect(() => {
    if (previousStep.current !== currentStep) heading.current?.focus();
    previousStep.current = currentStep;
  }, [currentStep]);

  return (
    <div className={auth.flow}>
      <h1 ref={heading} tabIndex={-1}>
        {currentStep === "complete"
          ? t(($) => $.reset.completeTitle)
          : currentStep === "sent"
            ? t(($) => $.reset.sentTitle)
            : currentStep === "password"
              ? t(($) => $.reset.passwordTitle)
              : currentStep === "invalid"
                ? t(($) => $.reset.invalidLinkTitle)
                : currentStep === "unavailable"
                  ? t(($) => $.reset.checkUnavailableTitle)
                  : t(($) => $.reset.title)}
      </h1>
      <section className={s.stack}>
        {currentStep === "checking" && (
          <p className={s.muted} role="status">
            {t(($) => $.reset.checkingLink)}
          </p>
        )}
        {currentStep === "invalid" && (
          <p className={s.muted}>{t(($) => $.validation.invalidResetLink)}</p>
        )}
        {currentStep === "unavailable" && (
          <>
            <ErrorToast
              error={link.error}
              message={authValidation.resetPassword.message(link.error)}
            />
            <Button
              disabled={link.isFetching}
              onClick={() => void link.refetch()}
            >
              {t(($) => $.reset.retryLinkCheck)}
            </Button>
          </>
        )}
        {currentStep === "request" && (
          <>
            <Form
              action={request}
              validation={authValidation.resetRequest}
              onSubmit={(data) =>
                request.run(async () => {
                  await result(
                    client.POST("/api/v1/auth/reset/request", {
                      body: { email: text(data, "email") },
                    }),
                  );
                  setStep("sent");
                })
              }
            >
              <Field
                name="email"
                label={t(($) => $.reset.email)}
                type="email"
                autoComplete="username"
                placeholder={t(($) => $.signIn.emailPlaceholder)}
              />
              <BusyButton busy={request.isPending}>
                {t(($) => $.reset.send)}
              </BusyButton>
            </Form>
          </>
        )}
        {currentStep === "password" && token && (
          <>
            <Form
              action={completion}
              validation={authValidation.resetPassword}
              onSubmit={(data) =>
                completion.run(async () => {
                  await result(
                    client.POST("/api/v1/auth/reset/complete", {
                      body: { token, password: text(data, "password") },
                    }),
                  );
                  setStep("complete");
                })
              }
            >
              {(error) => (
                <>
                  <div className={auth.feedbackGroup}>
                    <Field
                      name="password"
                      label={t(($) => $.reset.password)}
                      type="password"
                      autoComplete="new-password"
                      placeholder={t(($) => $.reset.passwordPlaceholder)}
                    />
                    {error && (
                      <p className={field().error} role="alert">
                        {error}
                      </p>
                    )}
                  </div>
                  <BusyButton busy={completion.isPending}>
                    {t(($) => $.reset.submit)}
                  </BusyButton>
                </>
              )}
            </Form>
          </>
        )}
        {currentStep === "sent" && (
          <p className={s.muted} role="status">
            {t(($) => $.reset.sent)}
          </p>
        )}
        {currentStep === "complete" && (
          <p className={s.muted} role="status">
            {t(($) => $.reset.complete)}
          </p>
        )}
        <div className={auth.secondaryActions}>
          <Link to="/login" className={auth.secondaryLink}>
            {t(($) => $.backToSignIn)}
          </Link>
        </div>
      </section>
    </div>
  );
}
