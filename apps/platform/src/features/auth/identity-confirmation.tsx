import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { client, result, setSession } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { useSessionRequest } from "../../api/use-session-request";
import { Input } from "../../components/input";
import { LinkButton } from "../../components/link";
import { PasswordInput } from "../../components/password-input";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import { AuthenticatorInput } from "./authenticator-input";
import { useAuthValidation } from "./use-auth-validation";

export function IdentityConfirmation({
  onConfirmed,
}: {
  onConfirmed: () => void;
}) {
  const { t } = useTranslation("auth");
  const queries = useQueryClient();
  const user = useQuery(sessionOptions).data?.user;
  const action = useAction();
  const request = useSessionRequest();
  const validation = useAuthValidation();
  const [recovery, setRecovery] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    let mounted = true;
    // Run after the outer task heading when confirmation mounts with a new stage.
    queueMicrotask(() => {
      if (mounted) heading.current?.focus();
    });
    return () => {
      mounted = false;
    };
  }, []);
  return (
    <div className={s.stack}>
      <h2 ref={heading} tabIndex={-1}>
        {t(($) => $.onboarding.identityTitle)}
      </h2>
      <p className={s.muted}>{t(($) => $.onboarding.identityDescription)}</p>
      <Form
        action={action}
        validation={validation.identity(Boolean(user?.mfaEnabled), recovery)}
        onSubmit={(data) =>
          action.run(() =>
            request(
              (signal) =>
                result(
                  client.POST("/api/v1/auth/reauthenticate", {
                    signal,
                    body: {
                      password: text(data, "password"),
                      code: user?.mfaEnabled ? text(data, "code") : undefined,
                    },
                  }),
                ),
              (next) => {
                queries.setQueryData(sessionOptions.queryKey, setSession(next));
                onConfirmed();
              },
            ),
          )
        }
      >
        <Field
          name="password"
          type="password"
          autoComplete="current-password"
          inputComponent={PasswordInput}
          label={t(($) => $.onboarding.password)}
        />
        {user?.mfaEnabled && (
          <Field
            key={recovery ? "recovery" : "authenticator"}
            name="code"
            label={
              recovery
                ? t(($) => $.mfa.recoveryCode)
                : t(($) => $.onboarding.authenticationCode)
            }
            inputComponent={recovery ? Input : AuthenticatorInput}
            autoComplete="one-time-code"
          />
        )}
        <BusyButton busy={action.isPending}>
          {t(($) => $.onboarding.confirmIdentity)}
        </BusyButton>
      </Form>
      {user?.mfaEnabled && (
        <LinkButton
          disabled={action.isPending}
          onClick={() => {
            action.reset();
            setRecovery(!recovery);
          }}
        >
          {recovery
            ? t(($) => $.mfa.useAuthenticator)
            : t(($) => $.mfa.useRecovery)}
        </LinkButton>
      )}
    </div>
  );
}
