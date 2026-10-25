import { useQuery } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { client, result, setSession } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { Link } from "../../components/link";
import { PasswordInput } from "../../components/password-input";
import { hostingOptions } from "../../hosting";
import { usePageTitle } from "../../i18n/use-page-title";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as authStyles from "./auth.styles";
import { MFAChallenge } from "./mfa-challenge";
import { useAuthValidation } from "./use-auth-validation";

export function SignIn({ returnTo }: { returnTo?: string }) {
  const { t } = useTranslation("auth");
  const authValidation = useAuthValidation();
  const session = useQuery(sessionOptions).data;
  const router = useRouter();
  const signedIn = Boolean(session?.user && !session.mfaPending);
  usePageTitle(
    session?.mfaPending
      ? t(($) => $.mfa.pageTitle)
      : t(($) => $.signIn.pageTitle),
  );
  useEffect(() => {
    if (signedIn) router.history.replace(returnTo ?? "/");
  }, [signedIn, router, returnTo]);
  const hosting = useQuery(hostingOptions).data;
  const action = useAction();
  if (session?.mfaPending) return <MFAChallenge />;
  if (signedIn) return <p role="status">{t(($) => $.signIn.opening)}</p>;
  return (
    <div className={authStyles.flow}>
      <h1>{t(($) => $.signIn.title)}</h1>
      <section className={s.stack}>
        <Form
          action={action}
          validation={authValidation.signIn}
          onSubmit={(d) =>
            action.run(async () =>
              setSession(
                await result(
                  client.POST("/api/v1/auth/login", {
                    body: {
                      email: text(d, "email"),
                      password: text(d, "password"),
                    },
                  }),
                ),
              ),
            )
          }
        >
          <Field
            name="email"
            label={t(($) => $.signIn.email)}
            type="email"
            autoComplete="username"
            placeholder={t(($) => $.signIn.emailPlaceholder)}
          />
          <Field
            name="password"
            label={t(($) => $.signIn.password)}
            type="password"
            autoComplete="current-password"
            inputComponent={PasswordInput}
            placeholder={t(($) => $.signIn.passwordPlaceholder)}
            labelAction={
              <Link
                to="/reset"
                variant="plain"
                className={authStyles.recoveryLink}
              >
                {t(($) => $.signIn.forgotPassword)}
              </Link>
            }
          />
          <BusyButton busy={action.isPending}>
            {t(($) => $.signIn.submit)}
          </BusyButton>
        </Form>
        {hosting?.enabled && (
          <div className={authStyles.secondaryActions}>
            <Link to="/signup" className={authStyles.secondaryLink}>
              {t(($) => $.signIn.hostedBeta)}
            </Link>
          </div>
        )}
      </section>
    </div>
  );
}
