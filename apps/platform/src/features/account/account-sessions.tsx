import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { cx } from "../../../styled-system/css";
import { accountSessionsOptions } from "../../api/account-sessions";
import { client, result, type Schema } from "../../api/client";
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
  return (
    <li className={styles.session}>
      <div className={styles.sessionDetails} id={id}>
        <p className={styles.sessionClient}>
          {t(($) => $.sessions.client, {
            browser: t(($) => $.sessions.browser[session.browser]),
            os: t(($) => $.sessions.os[session.os]),
          })}
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
  const [message, setMessage] = useState<"one" | "others">();
  const heading = useRef<HTMLHeadingElement>(null);
  const signOut = (
    kind: "one" | "others",
    send: (signal: AbortSignal) => Promise<Schema["AccountSessions"]>,
    origin: Element,
  ) => {
    setMessage(undefined);
    action.run(
      () =>
        request(send, (next) => {
          queries.setQueryData(options.queryKey, next);
          setMessage(kind);
          // The control that asked is gone, or has nothing left to do.
          heading.current?.focus();
        }).catch((error: unknown) => {
          // A session may have signed out meanwhile, so the list is read again.
          void queries.invalidateQueries({ queryKey: options.queryKey });
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
      {message && (
        <p role="status">
          {message === "one"
            ? t(($) => $.sessions.signedOut)
            : t(($) => $.sessions.othersSignedOut)}
        </p>
      )}
      <ErrorNotice
        error={action.error}
        message={t(($) => $.sessions.signOutFailed)}
      />
      {sessions.isPending ? (
        <p role="status">{t(($) => $.sessions.loading)}</p>
      ) : sessions.isError ? (
        <div className={styles.actions}>
          <ErrorNotice
            error={sessions.error}
            message={t(($) => $.sessions.loadFailed)}
          />
          <Button onClick={() => void sessions.refetch()}>
            {t(($) => $.sessions.retry)}
          </Button>
        </div>
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
                {action.isPending
                  ? t(($) => $.sessions.signingOut)
                  : t(($) => $.sessions.signOutOthers)}
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
