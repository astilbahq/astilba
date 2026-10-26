import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { client, result, setSession } from "../../api/client";
import { Input } from "../../components/input";
import { LinkButton } from "../../components/link";
import { ErrorToast } from "../../components/toast";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { AuthenticatorInput } from "./authenticator-input";
import { useAuthValidation } from "./use-auth-validation";

export function MFAChallenge() {
  const { t } = useTranslation("auth");
  const validation = useAuthValidation();
  const [recovery, setRecovery] = useState(false);
  const action = useAction();
  const cancellation = useAction();
  const heading = useRef<HTMLHeadingElement>(null);
  const busy = action.isPending || cancellation.isPending;
  const switchMode = () => {
    action.reset();
    setRecovery(!recovery);
  };

  useEffect(() => {
    heading.current?.focus();
  }, [recovery]);

  return (
    <div className={auth.flow}>
      <div className={auth.headingGroup}>
        <h1 ref={heading} tabIndex={-1}>
          {recovery ? t(($) => $.mfa.recoveryTitle) : t(($) => $.mfa.title)}
        </h1>
        <p className={s.muted}>
          {recovery
            ? t(($) => $.mfa.recoveryDescription)
            : t(($) => $.mfa.description)}
        </p>
      </div>
      <section className={s.stack}>
        <Form
          key={recovery ? "recovery" : "authenticator"}
          action={action}
          validation={recovery ? validation.recovery : validation.mfa}
          onSubmit={(data) => {
            if (busy) return;
            action.run(async () =>
              setSession(
                await result(
                  client.POST("/api/v1/auth/mfa", {
                    body: { code: text(data, "code") },
                  }),
                ),
              ),
            );
          }}
        >
          <Field
            name="code"
            inputComponent={recovery ? Input : AuthenticatorInput}
            label={
              recovery ? t(($) => $.mfa.recoveryCode) : t(($) => $.mfa.code)
            }
            placeholder={
              recovery ? t(($) => $.mfa.recoveryPlaceholder) : undefined
            }
            autoComplete="one-time-code"
            inputMode={recovery ? "text" : "numeric"}
            autoCapitalize="none"
            spellCheck={false}
          />
          <BusyButton busy={busy}>{t(($) => $.mfa.submit)}</BusyButton>
        </Form>
        <div className={auth.secondaryActions}>
          <LinkButton
            className={auth.secondaryLink}
            disabled={busy}
            onClick={switchMode}
          >
            {recovery
              ? t(($) => $.mfa.useAuthenticator)
              : t(($) => $.mfa.useRecovery)}
          </LinkButton>
          <LinkButton
            variant="subtle"
            className={auth.secondaryLink}
            disabled={busy}
            onClick={() =>
              cancellation.run(async () => {
                await result(client.POST("/api/v1/auth/logout"));
                // Discard cached session state and older in-flight session requests.
                window.location.reload();
              })
            }
          >
            {t(($) => $.mfa.useAnotherAccount)}
          </LinkButton>
          <ErrorToast
            error={cancellation.error}
            message={t(($) => $.validation.requestFailed)}
          />
        </div>
      </section>
    </div>
  );
}
