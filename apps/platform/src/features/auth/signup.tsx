import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { field } from "../../../styled-system/recipes";
import { client, result, setSession } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { useSessionRequest } from "../../api/use-session-request";
import { Button } from "../../components/button";
import { Link } from "../../components/link";
import { PasswordInput } from "../../components/password-input";
import { ErrorToast } from "../../components/toast";
import { hostingOptions } from "../../hosting";
import { usePageTitle } from "../../i18n/use-page-title";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { HostedOnboarding } from "./hosted-onboarding";
import { useAuthLinkCheck } from "./use-auth-link-check";
import { useAuthValidation } from "./use-auth-validation";

export function Signup({
  token,
  onCreated,
}: {
  token?: string;
  onCreated?: (workspaceId: string) => void;
}) {
  const { t } = useTranslation("auth");
  usePageTitle(t(($) => $.signup.pageTitle));
  const validation = useAuthValidation();
  const hosting = useQuery({ ...hostingOptions, retry: false });
  const session = useQuery(sessionOptions).data;
  const action = useAction();
  const request = useSessionRequest();
  const queries = useQueryClient();
  const [created, setCreated] = useState<string>();
  const link = useAuthLinkCheck(
    "signup",
    hosting.data?.enabled ? token : undefined,
  );
  const step = hosting.isPending
    ? "loading"
    : hosting.error && !hosting.data
      ? "hosting-error"
      : !hosting.data?.enabled
        ? "disabled"
        : !token
          ? "missing"
          : link.state === "ready"
            ? "details"
            : link.state;
  const heading = useRef<HTMLHeadingElement>(null);
  const previous = useRef(step);
  useEffect(() => {
    if (previous.current !== step) heading.current?.focus();
    previous.current = step;
  }, [step]);
  const returnTo = token
    ? `/signup?${new URLSearchParams({ token })}`
    : undefined;
  if (created) return <HostedOnboarding workspaceId={created} />;
  return (
    <div className={auth.flow}>
      <div className={auth.headingGroup}>
        <h1 ref={heading} tabIndex={-1}>
          {step === "invalid"
            ? t(($) => $.signup.invalidTitle)
            : step === "missing"
              ? t(($) => $.signup.missingTitle)
              : step === "disabled"
                ? t(($) => $.signup.disabledTitle)
                : step === "hosting-error" || step === "unavailable"
                  ? t(($) => $.signup.unavailableTitle)
                  : t(($) => $.signup.title)}
        </h1>
        {step === "details" && (
          <p className={s.muted}>{t(($) => $.signup.description)}</p>
        )}
      </div>
      <section className={s.stack}>
        {(step === "loading" || step === "checking") && (
          <p className={s.muted} role="status">
            {step === "loading"
              ? t(($) => $.signup.loading)
              : t(($) => $.signup.checking)}
          </p>
        )}
        {step === "disabled" && (
          <p className={s.muted}>{t(($) => $.signup.unavailable)}</p>
        )}
        {step === "missing" && (
          <p className={s.muted}>{t(($) => $.signup.missing)}</p>
        )}
        {step === "invalid" && (
          <p className={s.muted}>{t(($) => $.validation.invalidInvitation)}</p>
        )}
        {(step === "hosting-error" || step === "unavailable") && (
          <>
            <ErrorToast
              error={step === "hosting-error" ? hosting.error : link.error}
              message={validation
                .signup(Boolean(session?.user))
                .message(step === "hosting-error" ? hosting.error : link.error)}
            />
            <Button
              disabled={hosting.isFetching || link.isFetching}
              onClick={() =>
                void (step === "hosting-error"
                  ? hosting.refetch()
                  : link.refetch())
              }
            >
              {t(($) => $.signup.retry)}
            </Button>
          </>
        )}
        {step === "details" && token && !session?.mfaPending && (
          <>
            <p className={s.muted}>
              {session?.user ? (
                <Trans
                  t={t}
                  i18nKey={($) => $.signup.accepting}
                  components={{ email: <bdi>{session.user.email}</bdi> }}
                />
              ) : (
                t(($) => $.signup.signInFirst)
              )}
            </p>
            <Form
              action={action}
              label={t(($) => $.signup.form)}
              validation={validation.signup(Boolean(session?.user))}
              onSubmit={(data) =>
                action.run(() =>
                  request(
                    (signal) =>
                      result(
                        client.POST("/api/v1/hosting/signup", {
                          signal,
                          body: {
                            token,
                            workspace: text(data, "workspace"),
                            name: session?.user
                              ? undefined
                              : text(data, "name"),
                            password: session?.user
                              ? undefined
                              : text(data, "password"),
                          },
                        }),
                      ),
                    (next) => {
                      queries.setQueryData(
                        sessionOptions.queryKey,
                        setSession(next),
                      );
                      setCreated(next.createdWorkspaceId);
                      onCreated?.(next.createdWorkspaceId);
                    },
                  ),
                )
              }
            >
              {(error) => (
                <>
                  <Field
                    name="workspace"
                    label={t(($) => $.signup.workspace)}
                    placeholder={t(($) => $.signup.workspacePlaceholder)}
                    autoComplete="organization"
                  />
                  {!session?.user && (
                    <Field
                      name="name"
                      label={t(($) => $.signup.name)}
                      placeholder={t(($) => $.signup.namePlaceholder)}
                      autoComplete="name"
                    />
                  )}
                  <div className={auth.feedbackGroup}>
                    {!session?.user && (
                      <Field
                        name="password"
                        type="password"
                        inputComponent={PasswordInput}
                        label={t(($) => $.signup.password)}
                        placeholder={t(($) => $.signup.passwordPlaceholder)}
                        autoComplete="new-password"
                      />
                    )}
                    {error && (
                      <p className={field().error} role="alert">
                        {error}
                      </p>
                    )}
                  </div>
                  <BusyButton busy={action.isPending}>
                    {t(($) => $.signup.submit)}
                  </BusyButton>
                </>
              )}
            </Form>
          </>
        )}
        {
          <div className={auth.secondaryActions}>
            {session?.user ? (
              <Link to="/" className={auth.secondaryLink}>
                {t(($) => $.signup.backToProjects)}
              </Link>
            ) : (
              <Link
                to="/login"
                search={{ returnTo }}
                className={auth.secondaryLink}
              >
                {token ? t(($) => $.signup.signIn) : t(($) => $.backToSignIn)}
              </Link>
            )}
          </div>
        }
      </section>
    </div>
  );
}
