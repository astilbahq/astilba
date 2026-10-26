import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ApiError,
  client,
  result,
  type Schema,
  setSession,
} from "../../api/client";
import { sessionOptions } from "../../api/session";
import { subscriptionOptions } from "../../api/subscription";
import { useSessionRequest } from "../../api/use-session-request";
import { Button } from "../../components/button";
import { Link } from "../../components/link";
import { ErrorToast } from "../../components/toast";
import { hostingOptions } from "../../hosting";
import { usePageTitle } from "../../i18n/use-page-title";
import * as s from "../../styles";
import { useAction } from "../../ui";
import * as auth from "./auth.styles";
import { AuthenticatorSetup } from "./authenticator-setup";
import { IdentityConfirmation } from "./identity-confirmation";
import { RecoveryCodesConfirmation } from "./recovery-codes-confirmation";
import { useAuthValidation } from "./use-auth-validation";

type BillingReturn = "returned" | "cancelled";

function RecoveryStep({
  data,
  onCreated,
  onSaved,
  onResume,
}: {
  data?: Schema["RecoveryCodes"];
  onCreated: (data: Schema["RecoveryCodes"] | undefined) => void;
  onSaved: () => void;
  onResume: () => void;
}) {
  const { t } = useTranslation("auth");
  const session = useQuery(sessionOptions).data;
  const queries = useQueryClient();
  const action = useAction();
  const request = useSessionRequest();
  const validation = useAuthValidation();
  const regenerate = () => {
    onCreated(undefined);
    onResume();
    action.run(() =>
      request(
        (signal) =>
          result(client.POST("/api/v1/auth/mfa/recovery-codes", { signal })),
        (next) => {
          queries.setQueryData(
            sessionOptions.queryKey,
            setSession(next.session),
          );
          onCreated(next);
        },
      ),
    );
  };
  const needsIdentity =
    !session?.recentAuth ||
    (action.error instanceof ApiError &&
      action.error.detail.code === "recent_auth_required");
  if (!data && needsIdentity)
    return (
      <IdentityConfirmation
        onConfirmed={() => {
          action.reset();
          onResume();
        }}
      />
    );
  return (
    <div className={s.stack}>
      <ErrorToast
        error={action.error}
        message={validation.setup.message(action.error)}
      />
      {data ? (
        <RecoveryCodesConfirmation
          key={data.generation}
          data={data}
          replacing={action.isPending}
          onSaved={onSaved}
          onReplace={regenerate}
        />
      ) : (
        <>
          <p className={s.muted}>{t(($) => $.onboarding.lostCodes)}</p>
          <Button disabled={action.isPending} onClick={regenerate}>
            {t(($) => $.onboarding.replace)}
          </Button>
        </>
      )}
    </div>
  );
}

function SubscriptionStep({
  workspaceId,
  status,
  billing,
  refresh,
  onResume,
}: {
  workspaceId: string;
  status: Schema["Subscription"];
  billing?: BillingReturn;
  refresh: () => Promise<unknown>;
  onResume: () => void;
}) {
  const { t } = useTranslation("auth");
  const session = useQuery(sessionOptions).data;
  const action = useAction();
  const request = useSessionRequest();
  const validation = useAuthValidation();
  if (
    status.billingEnabled &&
    billing !== "returned" &&
    (!session?.recentAuth ||
      (action.error instanceof ApiError &&
        action.error.detail.code === "recent_auth_required"))
  )
    return (
      <IdentityConfirmation
        onConfirmed={() => {
          action.reset();
          onResume();
        }}
      />
    );
  return (
    <div className={s.stack}>
      {billing === "cancelled" && (
        <p className={s.muted}>{t(($) => $.onboarding.cancelled)}</p>
      )}
      {!status.billingEnabled ? (
        <>
          <p className={s.muted}>{t(($) => $.onboarding.billingUnavailable)}</p>
          {status.support && <p className={s.muted}>{status.support}</p>}
        </>
      ) : billing === "returned" ? (
        <>
          <p className={s.muted}>{t(($) => $.onboarding.pending)}</p>
          <Button
            disabled={action.isPending}
            onClick={() => action.run(refresh)}
          >
            {t(($) => $.onboarding.checkStatus)}
          </Button>
          <Link to="/onboarding" search={{ workspace: workspaceId }}>
            {t(($) => $.onboarding.backToSubscription)}
          </Link>
        </>
      ) : (
        <>
          <p>
            {t(($) => $.onboarding.allowance, { count: status.serverLimit })}
          </p>
          <p className={s.muted}>{t(($) => $.onboarding.billingDescription)}</p>
          <Button
            disabled={action.isPending}
            onClick={() =>
              action.run(() =>
                request(
                  (signal) =>
                    result(
                      client.POST(
                        "/api/v1/workspaces/{workspaceId}/subscription/checkout",
                        { signal, params: { path: { workspaceId } } },
                      ),
                    ),
                  (next) => window.location.assign(next.url),
                ),
              )
            }
          >
            {t(($) => $.onboarding.checkout)}
          </Button>
        </>
      )}
      <ErrorToast
        error={action.error}
        message={validation.setup.message(action.error)}
      />
    </div>
  );
}

function Onboarding({
  workspaceId,
  billing,
}: {
  workspaceId?: string;
  billing?: BillingReturn;
}) {
  const { t } = useTranslation("auth");
  const session = useQuery(sessionOptions).data;
  const user = !session?.mfaPending ? session?.user : undefined;
  const hosting = useQuery({ ...hostingOptions, retry: false });
  const workspace = session?.workspaces.find(
    (item) => item.id === workspaceId && item.role === "owner",
  );
  const subscription = useQuery(
    subscriptionOptions(
      user && workspace && hosting.data?.enabled ? workspaceId : undefined,
    ),
  );
  const [codes, setCodes] = useState<Schema["RecoveryCodes"]>();
  const stage = !user
    ? "guest"
    : hosting.isPending
      ? "loading"
      : hosting.error && !hosting.data
        ? "unavailable"
        : !hosting.data?.enabled
          ? "self-hosted"
          : !workspace
            ? "missing"
            : subscription.isPending
              ? "loading"
              : subscription.error && !subscription.data
                ? "unavailable"
                : !user.mfaEnabled
                  ? "authenticator"
                  : !user.recoveryCodesSaved ||
                      user.recoveryCodesRemaining === 0
                    ? "recovery"
                    : !user.emailVerified
                      ? "email"
                      : subscription.data?.managementActive
                        ? "complete"
                        : "subscription";
  const index =
    stage === "authenticator"
      ? 1
      : stage === "recovery"
        ? 2
        : stage === "subscription" || stage === "email"
          ? 3
          : stage === "complete"
            ? 4
            : -1;
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    heading.current?.focus();
  }, [stage]);
  useEffect(() => {
    if (!user?.mfaEnabled) setCodes(undefined);
  }, [user?.mfaEnabled]);
  const error = hosting.error ?? subscription.error;
  const validation = useAuthValidation();
  return (
    <div className={auth.flow}>
      <h1 ref={heading} tabIndex={-1}>
        {stage === "authenticator"
          ? t(($) => $.onboarding.authenticatorTitle)
          : stage === "recovery"
            ? t(($) => $.onboarding.recoveryTitle)
            : stage === "subscription"
              ? t(($) => $.onboarding.subscriptionTitle)
              : stage === "complete"
                ? t(($) => $.onboarding.completeTitle)
                : stage === "missing"
                  ? t(($) => $.onboarding.missingTitle)
                  : stage === "unavailable"
                    ? t(($) => $.onboarding.unavailable)
                    : stage === "email"
                      ? t(($) => $.onboarding.emailTitle)
                      : t(($) => $.onboarding.loadingTitle)}
      </h1>
      {index >= 0 && (
        <ol
          className={auth.progress}
          aria-label={t(($) => $.onboarding.progress)}
        >
          {[
            t(($) => $.onboarding.account),
            t(($) => $.onboarding.authenticator),
            t(($) => $.onboarding.recovery),
            t(($) => $.onboarding.subscription),
          ].map((label, item) => (
            <li
              key={item}
              aria-current={item === index ? "step" : undefined}
              data-complete={item < index}
            >
              {label}
              {item < index && (
                <span className={s.screenReaderOnly}>
                  {" "}
                  {t(($) => $.onboarding.completedStep)}
                </span>
              )}
            </li>
          ))}
        </ol>
      )}
      {stage === "loading" && (
        <p className={s.muted} role="status">
          {t(($) => $.onboarding.loading)}
        </p>
      )}
      {stage === "guest" && (
        <>
          <p className={s.muted}>{t(($) => $.onboarding.guest)}</p>
          <Link
            to="/login"
            search={{
              returnTo: `/onboarding${workspaceId ? `?${new URLSearchParams({ workspace: workspaceId, ...(billing ? { billing } : {}) })}` : ""}`,
            }}
          >
            {t(($) => $.onboarding.signIn)}
          </Link>
        </>
      )}
      {stage === "self-hosted" && (
        <p className={s.muted}>{t(($) => $.onboarding.selfHosted)}</p>
      )}
      {stage === "missing" && (
        <>
          <p className={s.muted}>{t(($) => $.onboarding.missing)}</p>
          <Link to="/members">{t(($) => $.onboarding.settings)}</Link>
        </>
      )}
      {stage === "unavailable" && (
        <>
          <ErrorToast error={error} message={validation.setup.message(error)} />
          <Button
            disabled={hosting.isFetching || subscription.isFetching}
            onClick={() =>
              void (hosting.error && !hosting.data
                ? hosting.refetch()
                : subscription.refetch())
            }
          >
            {t(($) => $.onboarding.retry)}
          </Button>
        </>
      )}
      {stage === "authenticator" && (
        <>
          <p className={s.muted}>
            {t(($) => $.onboarding.authenticatorDescription)}
          </p>
          <AuthenticatorSetup
            onEnrolled={setCodes}
            onResume={() => heading.current?.focus()}
          />
        </>
      )}
      {stage === "recovery" && (
        <RecoveryStep
          data={codes}
          onCreated={setCodes}
          onSaved={() => setCodes(undefined)}
          onResume={() => heading.current?.focus()}
        />
      )}
      {stage === "email" && (
        <>
          <p className={s.muted}>{t(($) => $.onboarding.emailDescription)}</p>
          <Link to="/verify-email">{t(($) => $.onboarding.verifyEmail)}</Link>
        </>
      )}
      {stage === "subscription" && subscription.data && workspaceId && (
        <SubscriptionStep
          status={subscription.data}
          workspaceId={workspaceId}
          billing={billing}
          onResume={() => heading.current?.focus()}
          refresh={async () => {
            const next = await subscription.refetch();
            if (next.error) throw next.error;
          }}
        />
      )}
      {stage === "complete" && (
        <p className={s.muted} role="status">
          {t(($) => $.onboarding.complete)}
        </p>
      )}
      {user && (
        <div className={auth.secondaryActions}>
          <Link
            to="/"
            search={{ workspace: workspaceId }}
            className={auth.secondaryLink}
          >
            {stage === "complete" || stage === "self-hosted"
              ? t(($) => $.onboarding.projects)
              : t(($) => $.onboarding.later)}
          </Link>
        </div>
      )}
    </div>
  );
}

export function HostedOnboarding(props: {
  workspaceId?: string;
  billing?: BillingReturn;
}) {
  const { t } = useTranslation("auth");
  usePageTitle(t(($) => $.onboarding.pageTitle));
  const session = useQuery(sessionOptions).data;
  return (
    <Onboarding
      key={`${session?.user?.id ?? "guest"}:${props.workspaceId ?? "missing"}`}
      {...props}
    />
  );
}
