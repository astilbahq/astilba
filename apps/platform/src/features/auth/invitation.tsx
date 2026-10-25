import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { client, result, setSession } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { Link } from "../../components/link";
import { usePageTitle } from "../../i18n/use-page-title";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { useAuthValidation } from "./use-auth-validation";

export function Invitation({ token }: { token?: string }) {
  const { t } = useTranslation("auth");
  usePageTitle(t(($) => $.invitation.pageTitle));
  const authValidation = useAuthValidation();
  const action = useAction();
  const session = useQuery(sessionOptions).data;
  const [done, setDone] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const returnTo = token
    ? `/invitation?${new URLSearchParams({ token })}`
    : undefined;

  useEffect(() => {
    if (done) heading.current?.focus();
  }, [done]);

  return (
    <div className={auth.flow}>
      <div className={auth.headingGroup}>
        <h1 ref={heading} tabIndex={-1}>
          {done
            ? t(($) => $.invitation.completeTitle)
            : t(($) => $.invitation.title)}
        </h1>
        {!done && token && (
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
        {!token ? (
          <p className={s.muted}>{t(($) => $.invitation.missingToken)}</p>
        ) : done ? (
          <p className={s.muted} role="status">
            {t(($) => $.invitation.complete)}
          </p>
        ) : (
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
              {!session?.user && (
                <>
                  <Field
                    name="name"
                    label={t(($) => $.invitation.name)}
                    autoComplete="name"
                    placeholder={t(($) => $.invitation.namePlaceholder)}
                  />
                  <Field
                    name="password"
                    label={t(($) => $.invitation.password)}
                    type="password"
                    autoComplete="new-password"
                    placeholder={t(($) => $.invitation.passwordPlaceholder)}
                  />
                </>
              )}
              <BusyButton busy={action.isPending}>
                {t(($) => $.invitation.submit)}
              </BusyButton>
            </Form>
          </>
        )}
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
