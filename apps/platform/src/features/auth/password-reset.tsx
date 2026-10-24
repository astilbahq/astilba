import { useEffect, useRef, useState } from "react";
import { client, result } from "../../api/client";
import { Link } from "../../components/link";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { authValidation } from "./validation";

type Step = "request" | "sent" | "password" | "complete";

export function PasswordReset({ token }: { token?: string }) {
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
          ? "Password changed"
          : step === "sent"
            ? "Check your email"
            : step === "password"
              ? "Set a new password"
              : "Reset your password"}
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
                label="Account email"
                type="email"
                autoComplete="username"
                placeholder="you@example.com"
                variant="quiet"
              />
              <BusyButton busy={request.isPending}>Send reset link</BusyButton>
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
                label="New password"
                type="password"
                autoComplete="new-password"
                placeholder="Enter a new password"
                variant="quiet"
              />
              <BusyButton busy={completion.isPending}>Set password</BusyButton>
            </Form>
          </>
        )}
        {step === "sent" && (
          <p className={s.muted} role="status">
            If this account exists and email is configured, a password reset
            link will arrive shortly.
          </p>
        )}
        {step === "complete" && (
          <p className={s.muted} role="status">
            You can now sign in with your new password.
          </p>
        )}
        <div className={auth.secondaryActions}>
          <Link to="/login" className={auth.secondaryLink}>
            Back to sign in
          </Link>
        </div>
      </section>
    </div>
  );
}
