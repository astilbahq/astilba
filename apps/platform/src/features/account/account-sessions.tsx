import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { cx } from "../../../styled-system/css";
import { accountSessionsOptions } from "../../api/account-sessions";
import { ApiError, client, result, type Schema } from "../../api/client";
import { isRecentAuthRequired } from "../../api/step-up";
import { useSessionRequest } from "../../api/use-session-request";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { ErrorNotice, useAction } from "../../ui";
import * as styles from "./account.styles";

const when = { dateStyle: "medium", timeStyle: "short" } as const;

function SessionItem({
  session,
  busy,
  onSignOut,
}: {
  session: Schema["AccountSession"];
  busy: boolean;
  onSignOut: (origin: Element) => void;
}) {
  const { t } = useTranslation("account");
  const id = useId();
  const signedInAt = new Date(session.signedInAt);
  const lastSeenAt = new Date(session.lastSeenAt);
  const client = t(($) => $.sessions.client, {
    browser: t(($) => $.sessions.browser[session.browser]),
    os: t(($) => $.sessions.os[session.os]),
  });
  return (
    <li className={styles.session}>
      <div className={styles.sessionDetails} id={id}>
        <p className={styles.sessionClient}>
          {client}
          {session.current && (
            <span className={styles.sessionCurrent}>
              {t(($) => $.sessions.current)}
            </span>
          )}
        </p>
        <p className={s.muted}>
          {session.network ? (
            <Trans
              t={t}
              i18nKey={($) => $.sessions.network}
              components={{ network: <bdi>{session.network}</bdi> }}
            />
          ) : (
            t(($) => $.sessions.networkUnknown)
          )}
        </p>
        <p className={s.muted}>
          {t(($) => $.sessions.signedIn, {
            at: signedInAt,
            formatParams: { at: when },
          })}
        </p>
        <p className={s.muted}>
          {session.current
            ? t(($) => $.sessions.activeNow)
            : t(($) => $.sessions.lastActive, {
                at: lastSeenAt,
                formatParams: { at: when },
              })}
        </p>
      </div>
      {!session.current && (
        <Button
          size="sm"
          aria-label={t(($) => $.sessions.signOutNamed, { client })}
          aria-describedby={id}
          disabled={busy}
          onClick={(event) => onSignOut(event.currentTarget)}
        >
          {t(($) => $.sessions.signOut)}
        </Button>
      )}
    </li>
  );
}

/**
 * The account's signed-in sessions, with sign out for each other one and for all of them. Each
 * sign-out needs recent authentication, which useAction asks for and then retries.
 */
export function SessionsSection({ user }: { user: Schema["Account"] }) {
  const { t } = useTranslation("account");
  const queries = useQueryClient();
  const options = accountSessionsOptions(user.id);
  const sessions = useQuery(options);
  const request = useSessionRequest();
  const action = useAction({ recentAuth: true });
  const [kind, setKind] = useState<"one" | "others">("one");
  const [message, setMessage] = useState<"one" | "others" | "gone">();
  const heading = useRef<HTMLHeadingElement>(null);
  // The control that asked, which a failure returns focus to once it is enabled again.
  const returnTo = useRef<Element>(null);
  useEffect(() => {
    if (action.isPending || !action.isError) return;
    const target = returnTo.current;
    returnTo.current = null;
    const active = document.activeElement;
    if (active && active !== document.body && active.isConnected) return;
    (target instanceof HTMLElement && target.isConnected
      ? target
      : heading.current
    )?.focus();
  }, [action.isPending, action.isError]);
  const signedOut = (outcome: "one" | "others" | "gone") => {
    setMessage(outcome);
    returnTo.current = null;
    // The control that asked is gone, or has nothing left to do.
    heading.current?.focus();
  };
  const signOut = (
    asked: "one" | "others",
    send: (signal: AbortSignal) => Promise<Schema["AccountSessions"]>,
    origin: Element,
  ) => {
    setMessage(undefined);
    setKind(asked);
    returnTo.current = origin;
    action.run(
      () =>
        request(send, (next) => {
          queries.setQueryData(options.queryKey, next);
          signedOut(asked);
        }).catch(async (error: unknown) => {
          // Refused before anything changed: useAction confirms identity and asks again.
          if (isRecentAuthRequired(error)) throw error;
          // A session may have signed out meanwhile, so the list is read again.
          const reread = queries.invalidateQueries({
            queryKey: options.queryKey,
          });
          if (
            asked === "one" &&
            error instanceof ApiError &&
            error.detail.code === "not_found"
          ) {
            // Signed out elsewhere, or expired: what was asked for is done.
            await reread;
            signedOut("gone");
            return;
          }
          throw error;
        }),
      origin,
    );
  };
  const others =
    (sessions.data?.sessions.filter((session) => !session.current).length ??
      0) + (sessions.data?.unlisted ?? 0);
  return (
    <section className={styles.section} aria-labelledby="account-sessions">
      <div className={styles.intro}>
        <h2 id="account-sessions" ref={heading} tabIndex={-1}>
          {t(($) => $.sessions.title)}
        </h2>
        <p className={s.muted}>{t(($) => $.sessions.description)}</p>
      </div>
      {action.isPending ? (
        <p role="status">{t(($) => $.sessions.signingOut)}</p>
      ) : (
        message && (
          <p role="status">
            {message === "one"
              ? t(($) => $.sessions.signedOut)
              : message === "others"
                ? t(($) => $.sessions.othersSignedOut)
                : t(($) => $.sessions.alreadySignedOut)}
          </p>
        )
      )}
      <ErrorNotice
        error={action.error}
        message={
          kind === "one"
            ? t(($) => $.sessions.signOutFailed)
            : t(($) => $.sessions.othersSignOutFailed)
        }
      />
      {!sessions.data ? (
        sessions.isError ? (
          <div className={styles.actions}>
            <ErrorNotice
              error={sessions.error}
              message={t(($) => $.sessions.loadFailed)}
            />
            <Button
              disabled={sessions.isFetching}
              onClick={() => void sessions.refetch()}
            >
              {t(($) => $.sessions.retry)}
            </Button>
          </div>
        ) : (
          <p role="status">{t(($) => $.sessions.loading)}</p>
        )
      ) : (
        <>
          <ul className={cx(s.list, styles.sessions)}>
            {sessions.data.sessions.map((session) => (
              <SessionItem
                key={session.id}
                session={session}
                busy={action.isPending}
                onSignOut={(origin) =>
                  signOut(
                    "one",
                    (signal) =>
                      result(
                        client.DELETE("/api/v1/auth/sessions/{sessionId}", {
                          signal,
                          params: { path: { sessionId: session.id } },
                        }),
                      ),
                    origin,
                  )
                }
              />
            ))}
          </ul>
          {sessions.data.unlisted > 0 && (
            <p className={s.muted}>
              {t(($) => $.sessions.unlisted, {
                count: sessions.data.unlisted,
              })}
            </p>
          )}
          {others > 0 ? (
            <div className={styles.actions}>
              <Button
                variant="danger"
                disabled={action.isPending}
                onClick={(event) =>
                  signOut(
                    "others",
                    (signal) =>
                      result(
                        client.POST("/api/v1/auth/sessions/sign-out-others", {
                          signal,
                        }),
                      ),
                    event.currentTarget,
                  )
                }
              >
                {t(($) => $.sessions.signOutOthers)}
              </Button>
            </div>
          ) : (
            <p className={s.muted}>{t(($) => $.sessions.noOthers)}</p>
          )}
        </>
      )}
    </section>
  );
}
