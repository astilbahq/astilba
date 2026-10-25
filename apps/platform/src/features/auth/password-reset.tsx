import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { client, result } from "../../api/client";
import { Link } from "../../components/link";
import { usePageTitle } from "../../i18n/use-page-title";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { useAuthValidation } from "./use-auth-validation";

type Step = "request" | "sent" | "password" | "complete";

export function PasswordReset({ token }: { token?: string }) {
  const { t } = useTranslation("auth");
  usePageTitle(t(($) => $.reset.pageTitle));
  const authValidation = useAuthValidation();
  const [step, setStep] = useState<Step>(token ? "password" : "request");
  const request = useAction();
  const completion = useAction();
  const heading = useRef<HTMLHeadingElement>(null);
  const previousStep = useRef(step);

  useEffect(() => {
    if (previousStep.current !== step) heading.current?.focus();
    previousStep.current = step;
  }, [step]);

  return (
    <div className={auth.flow}>
      <h1 ref={heading} tabIndex={-1}>
        {step === "complete"
          ? t(($) => $.reset.completeTitle)
          : step === "sent"
            ? t(($) => $.reset.sentTitle)
            : step === "password"
              ? t(($) => $.reset.passwordTitle)
              : t(($) => $.reset.title)}
      </h1>
      <section className={s.stack}>
        {step === "request" && (
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
        {step === "password" && token && (
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
              <Field
                name="password"
                label={t(($) => $.reset.password)}
                type="password"
                autoComplete="new-password"
                placeholder={t(($) => $.reset.passwordPlaceholder)}
              />
              <BusyButton busy={completion.isPending}>
                {t(($) => $.reset.submit)}
              </BusyButton>
            </Form>
          </>
        )}
        {step === "sent" && (
          <p className={s.muted} role="status">
            {t(($) => $.reset.sent)}
          </p>
        )}
        {step === "complete" && (
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
