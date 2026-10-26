import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { field } from "../../../styled-system/recipes";
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
import * as s from "../../styles";
import { BusyButton, Form, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { useAuthValidation } from "./use-auth-validation";

export function RecoveryCodesConfirmation({
  data,
  onSaved,
  onReplace,
  replacing,
}: {
  data: Schema["RecoveryCodes"];
  onSaved: () => void;
  onReplace: () => void;
  replacing: boolean;
}) {
  const { t } = useTranslation("auth");
  const queries = useQueryClient();
  const action = useAction();
  const request = useSessionRequest();
  const validation = useAuthValidation();
  const [stale, setStale] = useState(false);
  if (stale)
    return (
      <div className={s.stack}>
        <p className={field().error} role="alert">
          {t(($) => $.onboarding.changedCodes)}
        </p>
        <Button disabled={replacing} onClick={onReplace}>
          {t(($) => $.onboarding.replace)}
        </Button>
      </div>
    );
  return (
    <div className={s.stack}>
      <p className={s.muted}>{t(($) => $.onboarding.saveDescription)}</p>
      <ul
        className={auth.recoveryCodes}
        aria-label={t(($) => $.onboarding.codesLabel)}
      >
        {data.codes.map((code) => (
          <li key={code}>
            <code>{code}</code>
          </li>
        ))}
      </ul>
      <Form
        action={action}
        validation={validation.setup}
        onSubmit={() =>
          action.run(() =>
            request(
              async (signal) => {
                try {
                  return await result(
                    client.POST("/api/v1/auth/mfa/recovery-codes/acknowledge", {
                      signal,
                      body: { generation: data.generation },
                    }),
                  );
                } catch (error) {
                  if (
                    error instanceof ApiError &&
                    error.detail.code === "conflict"
                  )
                    setStale(true);
                  throw error;
                }
              },
              (next) => {
                queries.setQueryData(sessionOptions.queryKey, setSession(next));
                onSaved();
              },
            ),
          )
        }
      >
        <BusyButton busy={action.isPending}>
          {t(($) => $.onboarding.saved)}
        </BusyButton>
      </Form>
    </div>
  );
}
