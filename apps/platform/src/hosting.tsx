import { useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";
import { client, result, setSession } from "./api/client";
import { sessionOptions } from "./api/session";
import { Button } from "./components/button";
import { Link } from "./components/link";
import * as authStyles from "./features/auth/auth.styles";
import { useAuthValidation } from "./features/auth/use-auth-validation";
import { usePageTitle } from "./i18n/use-page-title";
import * as s from "./styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";

export const hostingOptions = {
  queryKey: ["hosting"],
  queryFn: () => result(client.GET("/api/v1/hosting")),
};
function SecurityLink({ children }: { children?: ReactNode }) {
  return <Link to="/account">{children}</Link>;
}
function BillingLink({ children }: { children?: ReactNode }) {
  return <Link to="/members">{children}</Link>;
}
export function Signup() {
  const { t } = useTranslation("auth");
  const validation = useAuthValidation();
  usePageTitle(t(($) => $.signup.pageTitle));
  const hosting = useQuery(hostingOptions);
  const session = useQuery(sessionOptions).data;
  const action = useAction();
  const errorMessage = validation.invitation.message(action.error);
  const [done, setDone] = useState(false);
  if (!hosting.data?.enabled) return <p>{t(($) => $.signup.unavailable)}</p>;
  return (
    <div className={s.stack}>
      <h1>{t(($) => $.signup.title)}</h1>
      <p>{t(($) => $.signup.description)}</p>
      <ErrorNotice error={action.error} message={errorMessage} />
      {done ? (
        <p role="status">
          <Trans
            t={t}
            ns="auth"
            i18nKey={($) => $.signup.complete}
            components={{
              security: <SecurityLink />,
              billing: <BillingLink />,
            }}
          />
        </p>
      ) : (
        <section className={authStyles.formSection}>
          <Form
            action={action}
            errorMessage={errorMessage}
            label={t(($) => $.signup.form)}
            onSubmit={(d) =>
              action.run(async () => {
                setSession(
                  await result(
                    client.POST("/api/v1/hosting/signup", {
                      body: {
                        token: text(d, "token"),
                        workspace: text(d, "workspace"),
                        name: session?.user ? undefined : text(d, "name"),
                        password: session?.user
                          ? undefined
                          : text(d, "password"),
                      },
                    }),
                  ),
                );
                setDone(true);
              })
            }
          >
            <Field
              name="token"
              label={t(($) => $.signup.token)}
              autoComplete="off"
            />
            <Field name="workspace" label={t(($) => $.signup.workspace)} />
            {!session?.user && (
              <>
                <Field
                  name="name"
                  label={t(($) => $.signup.name)}
                  autoComplete="name"
                />
                <Field
                  name="password"
                  label={t(($) => $.signup.password)}
                  type="password"
                  autoComplete="new-password"
                  minLength={15}
                />
              </>
            )}
            {session?.user && (
              <p>
                <Trans
                  t={t}
                  i18nKey={($) => $.signup.accepting}
                  components={{ email: <bdi>{session.user.email}</bdi> }}
                />
              </p>
            )}
            <BusyButton busy={action.isPending}>
              {t(($) => $.signup.submit)}
            </BusyButton>
          </Form>
        </section>
      )}
    </div>
  );
}
export function EmailVerification() {
  const user = useQuery(sessionOptions).data?.user;
  const action = useAction();
  const [sent, setSent] = useState(false);
  if (user?.emailVerified) return <p>Email verified.</p>;
  return (
    <section className={s.panel}>
      <h2>Verify your email</h2>
      <p>Email verification is required for hosted workspace changes.</p>
      <ErrorNotice error={action.error} />
      <Button
        type="button"
        disabled={action.isPending}
        onClick={() =>
          action.run(async () => {
            await result(client.POST("/api/v1/auth/email/request"));
            setSent(true);
          })
        }
      >
        Send verification code
      </Button>
      {sent && (
        <p role="status">Check your email for a code valid for one hour.</p>
      )}
      <Form
        action={action}
        label="Verify email"
        onSubmit={(d) =>
          action.run(async () => {
            setSession(
              await result(
                client.POST("/api/v1/auth/email/verify", {
                  body: { token: text(d, "token") },
                }),
              ),
            );
          })
        }
      >
        <Field
          name="token"
          label="Email verification code"
          autoComplete="one-time-code"
        />
        <BusyButton busy={action.isPending}>Verify email</BusyButton>
      </Form>
    </section>
  );
}
export function WorkspaceSubscription({
  workspaceId,
  owner,
}: {
  workspaceId: string;
  owner: boolean;
}) {
  const action = useAction();
  const status = useQuery({
    queryKey: ["subscription", workspaceId],
    enabled: !!workspaceId,
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/subscription", {
          params: { path: { workspaceId } },
        }),
      ),
  });
  if (!status.data?.hosted) return <ErrorNotice error={status.error} />;
  const data = status.data;
  return (
    <section className={s.panel}>
      <h2>Workspace subscription</h2>
      <ErrorNotice error={action.error ?? status.error} />
      <p>
        Status: {data.status}. Server allowance: {data.serverLimit}. No
        per-application, database or member charges.
      </p>
      {data.managementUntil && (
        <p>
          Management access until{" "}
          {new Date(data.managementUntil).toLocaleString()}.
        </p>
      )}
      {data.recoveryUntil && (
        <p>
          Export, recovery and transfer remain available until{" "}
          {new Date(data.recoveryUntil).toLocaleString()}. Running applications
          and installed schedules remain on your servers.
        </p>
      )}
      <p>
        {data.support ||
          "Contact the beta operator for asynchronous support hours."}
      </p>
      {owner && data.billingEnabled && (
        <div className={s.row}>
          <Button
            type="button"
            disabled={action.isPending}
            onClick={() =>
              action.run(async () => {
                const v = await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/subscription/checkout",
                    { params: { path: { workspaceId } } },
                  ),
                );
                location.assign(v.url);
              })
            }
          >
            Start subscription
          </Button>
          <Button
            type="button"
            disabled={action.isPending}
            onClick={() =>
              action.run(async () => {
                const v = await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/subscription/portal",
                    { params: { path: { workspaceId } } },
                  ),
                );
                location.assign(v.url);
              })
            }
          >
            Manage billing
          </Button>
        </div>
      )}
      {owner && (
        <p>
          Billing changes require verified email, two-factor authentication and
          recent identity confirmation in{" "}
          <Link to="/account">account security</Link>.
        </p>
      )}
    </section>
  );
}
