import { useQueries, useQuery } from "@tanstack/react-query";
import type { RefObject } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, type Schema } from "../../api/client";
import { draftOptions, type EnvironmentPath } from "../../api/environment";
import { membersOptions } from "../../api/members";
import { serversOptions } from "../../api/servers";
import { sessionOptions } from "../../api/session";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { ErrorNotice } from "../../ui";
import * as styles from "./troubleshooting.styles";

type Session = Schema["TroubleshootingSession"];

export const shortId = (id: string) => id.slice(0, 8);

function useRefusalText() {
  const { t } = useTranslation("troubleshooting");
  return (reason: string | undefined) => {
    switch (reason) {
      case "installation":
        return t(($) => $.refusal.installation);
      case "environment":
        return t(($) => $.refusal.environment);
      case "not_deployed":
        return t(($) => $.refusal.notDeployed);
      case "agent_update":
        return t(($) => $.refusal.agentUpdate);
      case "runtime":
        return t(($) => $.refusal.runtime);
      case "host_not_ready":
        return t(($) => $.refusal.hostNotReady);
      case "host_opt_in":
        return t(($) => $.refusal.hostOptIn);
      case "limit":
        return t(($) => $.refusal.limit);
      case "ticket":
        return t(($) => $.refusal.ticket);
      case "elevated":
        return t(($) => $.refusal.elevated);
      case "opt_in":
        return t(($) => $.refusal.hostOptIn);
      case "ineligible":
        return t(($) => $.refusal.ineligible);
      case "no_shell":
        return t(($) => $.refusal.noShell);
      case "forbidden":
        return t(($) => $.refusal.forbidden);
      case "invalid_input":
        return t(($) => $.refusal.invalidInput);
      case "mfa_required":
        return t(($) => $.refusal.mfaRequired);
      case "email_verification_required":
        return t(($) => $.refusal.emailVerificationRequired);
      case "subscription_required":
        return t(($) => $.refusal.subscriptionRequired);
      default:
        return undefined;
    }
  };
}

// A refusal carries its reason in `fields`, which ErrorNotice would print as a
// machine value beside the message.
export function SessionErrorNotice({ error }: { error: unknown }) {
  const refusal = useRefusalText();
  if (
    error instanceof ApiError &&
    error.detail.code === "troubleshooting_refused"
  )
    return (
      <p className={s.error} role="alert">
        {refusal(error.detail.fields?.[0]) ?? error.message}
      </p>
    );
  return <ErrorNotice error={error} />;
}

export function SessionLoadFailed({
  message,
  heading,
  retry,
}: {
  message: string;
  heading: RefObject<HTMLHeadingElement | null>;
  retry: () => void;
}) {
  const { t } = useTranslation("troubleshooting");
  return (
    <div className={styles.part} role="alert">
      <p>{message}</p>
      <div>
        <Button
          onClick={() => {
            heading.current?.focus();
            retry();
          }}
        >
          {t(($) => $.retry)}
        </Button>
      </div>
    </div>
  );
}

const refusedPrefix = "session.refused.";

export function useAuditActionText() {
  const { t } = useTranslation("troubleshooting");
  const refusal = useRefusalText();
  return (action: string) => {
    if (!action.startsWith(refusedPrefix)) return action;
    const reason = refusal(action.slice(refusedPrefix.length));
    return reason
      ? t(($) => $.refusal.activity, { reason })
      : t(($) => $.refusal.activityUnknown);
  };
}

export function useSessionText() {
  const { t, i18n } = useTranslation("troubleshooting");
  return {
    date: (value: string) =>
      new Date(value).toLocaleString(i18n.resolvedLanguage),
    // History can hold an unclosed session past its deadline until the server
    // next sweeps it.
    state(session: Session) {
      if (session.state === "closed") return t(($) => $.state.closed);
      if (Date.parse(session.deadlineAt) <= Date.now())
        return t(($) => $.state.expired);
      return session.state === "active"
        ? t(($) => $.state.active)
        : t(($) => $.state.offered);
    },
    duration(session: Session) {
      if (!session.closedAt)
        return session.state === "active" &&
          Date.parse(session.deadlineAt) > Date.now()
          ? t(($) => $.duration.inProgress)
          : t(($) => $.duration.notRecorded);
      const count = Math.floor(
        (Date.parse(session.closedAt) - Date.parse(session.createdAt)) / 60000,
      );
      return count < 1
        ? t(($) => $.duration.underMinute)
        : t(($) => $.duration.minutes, { count });
    },
    closeReason(reason: string | undefined) {
      switch (reason) {
        case undefined:
          return t(($) => $.closeReason.none);
        case "closed":
          return t(($) => $.closeReason.closed);
        case "closed by owner":
          return t(($) => $.closeReason.closedByOwner);
        case "environment setting changed":
          return t(($) => $.closeReason.environmentSetting);
        case "expired":
          return t(($) => $.closeReason.expired);
        case "management recovery":
          return t(($) => $.closeReason.managementRecovery);
        default:
          return reason;
      }
    },
  };
}

// Sessions identify people, servers and services only by ID. Names that cannot be
// resolved fall back to a short ID: only owners can list members, and a service
// may have left the draft since the session ran.
export function useSessionNames(
  workspaceId: string,
  owner: boolean,
  environments: EnvironmentPath[],
  openSessions: Session[] = [],
) {
  const { t } = useTranslation("troubleshooting");
  const self = useQuery(sessionOptions).data?.user?.id;
  const servers = useQuery(serversOptions(workspaceId)).data;
  const members = useQuery({
    ...membersOptions(workspaceId),
    enabled: owner,
    // Someone can join and open a session after this list has loaded.
    refetchInterval: ({ state: { data } }) =>
      data &&
      openSessions.some(({ actorId }) => !data.some(({ id }) => id === actorId))
        ? 5000
        : false,
  }).data;
  const applications = useQueries({
    queries: environments.map((path) => draftOptions(path)),
    combine: (drafts) =>
      drafts.flatMap((draft) => draft.data?.specification.applications ?? []),
  });
  return {
    target: (session: Session) =>
      t(($) => $.target, {
        service:
          applications.find(({ id }) => id === session.applicationId)?.name ??
          t(($) => $.fallback.service, { id: shortId(session.applicationId) }),
        instance: session.instance,
      }),
    person: (session: Session) =>
      session.actorId === self
        ? t(($) => $.you)
        : (members?.find(({ id }) => id === session.actorId)?.name ??
          t(($) => $.fallback.member, { id: shortId(session.actorId) })),
    server: (session: Session) =>
      servers?.find(({ id }) => id === session.serverId)?.name ??
      t(($) => $.fallback.server, { id: shortId(session.serverId) }),
  };
}
