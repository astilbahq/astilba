import { useQuery, useQueryClient } from "@tanstack/react-query";
import { QRCodeSVG } from "qrcode.react";
import { useState } from "react";
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
import { Button } from "../../components/button";
import { Anchor, LinkButton } from "../../components/link";
import { ErrorToast } from "../../components/toast";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { AuthenticatorInput } from "./authenticator-input";
import { IdentityConfirmation } from "./identity-confirmation";
import { useAuthValidation } from "./use-auth-validation";

export function AuthenticatorSetup({
  onEnrolled,
  onResume,
}: {
  onEnrolled: (data: Schema["RecoveryCodes"]) => void;
  onResume: () => void;
}) {
  const { t } = useTranslation("auth");
  const session = useQuery(sessionOptions).data;
  const queries = useQueryClient();
  const begin = useAction();
  const request = useSessionRequest();
  const confirm = useAction();
  const validation = useAuthValidation();
  const [enrollment, setEnrollment] = useState<Schema["MFAEnrollment"]>();
  const error = confirm.error ?? begin.error;
  const needsIdentity =
    !session?.recentAuth ||
    (error instanceof ApiError && error.detail.code === "recent_auth_required");
  const startEnrollment = () => {
    confirm.reset();
    setEnrollment(undefined);
    onResume();
    begin.run(() =>
      request(
        (signal) => result(client.POST("/api/v1/auth/mfa/enroll", { signal })),
        setEnrollment,
      ),
    );
  };
  if (needsIdentity)
    return (
      <IdentityConfirmation
        onConfirmed={() => {
          begin.reset();
          confirm.reset();
          onResume();
        }}
      />
    );
  return (
    <div className={s.stack}>
      <ErrorToast
        error={begin.error}
        message={validation.setup.message(begin.error)}
      />
      {!enrollment ? (
        <Button disabled={begin.isPending} onClick={startEnrollment}>
          {t(($) => $.onboarding.begin)}
        </Button>
      ) : (
        <>
          <p className={s.muted}>{t(($) => $.onboarding.scan)}</p>
          <QRCodeSVG
            value={enrollment.uri}
            title={t(($) => $.onboarding.qrTitle)}
            size={224}
            marginSize={4}
            className={auth.authenticatorQR}
          />
          <div className={auth.feedbackGroup}>
            <span>{t(($) => $.onboarding.manualKey)}</span>
            <code className={auth.setupKey}>{enrollment.secret}</code>
          </div>
          <Anchor href={enrollment.uri}>
            {t(($) => $.onboarding.openAuthenticator)}
          </Anchor>
          <Form
            action={confirm}
            validation={validation.mfa}
            onSubmit={(data) =>
              confirm.run(() =>
                request(
                  (signal) =>
                    result(
                      client.POST("/api/v1/auth/mfa/confirm", {
                        signal,
                        body: { code: text(data, "code") },
                      }),
                    ),
                  (next) => {
                    queries.setQueryData(
                      sessionOptions.queryKey,
                      setSession(next.session),
                    );
                    onEnrolled(next);
                  },
                ),
              )
            }
          >
            <Field
              name="code"
              label={t(($) => $.onboarding.authenticationCode)}
              inputComponent={AuthenticatorInput}
              autoComplete="one-time-code"
            />
            <BusyButton busy={confirm.isPending}>
              {t(($) => $.onboarding.enable)}
            </BusyButton>
          </Form>
          <LinkButton
            disabled={begin.isPending || confirm.isPending}
            name="restartSetup"
            onClick={startEnrollment}
          >
            {t(($) => $.onboarding.restartAuthenticator)}
          </LinkButton>
        </>
      )}
    </div>
  );
}
