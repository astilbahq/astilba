import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  type MouseEvent,
  type RefObject,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import { ApiError, client, result } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { sessionOptions } from "../../api/session";
import {
  AlertDialogActions,
  AlertDialogClose,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogRoot,
  AlertDialogTitle,
} from "../../components/alert-dialog";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { useAction } from "../../ui";
import { openSessionsOptions, sessionsKey } from "./session-queries";
import {
  SessionErrorNotice,
  SessionLoadFailed,
  useSessionNames,
  useSessionText,
} from "./session-text";
import * as styles from "./troubleshooting.styles";

const workspacePath =
  "/api/v1/workspaces/{workspaceId}/troubleshooting-sessions";

export function OpenSessions({
  path,
  owner,
}: {
  path: EnvironmentPath;
  owner: boolean;
}) {
  const { t } = useTranslation("troubleshooting");
  const queries = useQueryClient();
  const action = useAction();
  const sessions = useQuery(openSessionsOptions(path));
  const self = useQuery(sessionOptions).data?.user?.id;
  const names = useSessionNames(path.workspaceId, owner, [path], sessions.data);
  const text = useSessionText();
  const [message, setMessage] = useState("");
  const heading = useRef<HTMLHeadingElement>(null);
  // These dialogs sit outside the list so that a poll cannot remove an open one.
  const [ending, setEnding] = useState<{ id: string; target: string }>();
  const [endingOpen, setEndingOpen] = useState(false);
  const [endingAll, setEndingAll] = useState(false);
  const returnTo = useRef<HTMLElement | null>(null);
  const dialogFocus = () =>
    returnTo.current?.isConnected ? returnTo.current : heading.current;
  // A failure to end one session stops applying once that session has gone.
  const [actedOn, setActedOn] = useState<string>();
  const actionOutdated =
    actedOn && sessions.data && !sessions.data.some(({ id }) => id === actedOn);
  const { workspaceId } = path;
  const refresh = () =>
    queries.invalidateQueries({ queryKey: sessionsKey(workspaceId) });
  const end = (sessionId: string) =>
    action.run(async () => {
      setMessage("");
      setActedOn(sessionId);
      try {
        await result(
          client.POST(`${workspacePath}/{sessionId}/close`, {
            params: { path: { workspaceId, sessionId } },
          }),
        );
        setMessage(t(($) => $.open.ended));
      } catch (error) {
        // Sessions also end on their own, so the list can be out of date.
        if (!(error instanceof ApiError && error.detail.code === "not_found"))
          throw error;
        setMessage(t(($) => $.open.alreadyEnded));
      } finally {
        await refresh();
      }
    });
  const endAll = () =>
    action.run(async () => {
      setMessage("");
      setActedOn(undefined);
      try {
        await result(
          client.POST(`${workspacePath}/close`, {
            params: { path: { workspaceId } },
          }),
        );
        setMessage(t(($) => $.open.allEnded));
      } finally {
        await refresh();
      }
    });
  return (
    <div className={styles.part}>
      <h3 ref={heading} tabIndex={-1} className={styles.heading}>
        {t(($) => $.open.title)}
      </h3>
      {!actionOutdated && <SessionErrorNotice error={action.error} />}
      {sessions.error && sessions.data && (
        <p className={s.error} role="alert">
          {t(($) => $.open.refreshFailed)}
        </p>
      )}
      {action.isPending ? (
        <p role="status">{t(($) => $.open.working)}</p>
      ) : (
        message && <p role="status">{message}</p>
      )}
      {sessions.isPending && <p role="status">{t(($) => $.open.loading)}</p>}
      {sessions.isError && !sessions.data && (
        <SessionLoadFailed
          message={t(($) => $.open.loadFailed)}
          heading={heading}
          retry={() => void sessions.refetch()}
        />
      )}
      {sessions.data?.length === 0 && <p>{t(($) => $.open.empty)}</p>}
      {!!sessions.data?.length && (
        <ul className={s.list}>
          {sessions.data.map((session) => {
            const target = names.target(session);
            return (
              <li key={session.id} className={styles.session}>
                <div className={styles.sessionDetails}>
                  <bdi className={styles.targetName}>{target}</bdi>
                  <p className={styles.metadata}>
                    <bdi>{names.person(session)}</bdi>
                    <bdi>{names.server(session)}</bdi>
                    <span>{text.state(session)}</span>
                  </p>
                  <p className={styles.metadata}>
                    <time dateTime={session.createdAt}>
                      {t(($) => $.open.started, {
                        date: text.date(session.createdAt),
                      })}
                    </time>
                    <time dateTime={session.deadlineAt}>
                      {t(($) => $.open.ends, {
                        date: text.date(session.deadlineAt),
                      })}
                    </time>
                  </p>
                </div>
                {(owner || session.actorId === self) && (
                  <EndSessionButton
                    label={t(($) => $.open.endNamed, { target })}
                    disabled={action.isPending}
                    heading={heading}
                    onClick={(event) => {
                      returnTo.current = event.currentTarget;
                      setEnding({ id: session.id, target });
                      setEndingOpen(true);
                    }}
                  />
                )}
              </li>
            );
          })}
        </ul>
      )}
      {owner && (
        <div className={styles.actions}>
          <Button
            disabled={action.isPending}
            onClick={(event) => {
              returnTo.current = event.currentTarget;
              setEndingAll(true);
            }}
          >
            {t(($) => $.open.endAll)}
          </Button>
        </div>
      )}
      <ConfirmEnd
        open={endingOpen}
        onOpenChange={setEndingOpen}
        title={t(($) => $.open.endTitle)}
        description={t(($) => $.open.endDescription, {
          target: ending?.target ?? "",
        })}
        confirm={t(($) => $.open.end)}
        finalFocus={dialogFocus}
        onConfirm={() => {
          // The ended session's row, and so its button, is about to disappear.
          returnTo.current = null;
          if (ending) end(ending.id);
        }}
      />
      <ConfirmEnd
        open={endingAll}
        onOpenChange={setEndingAll}
        title={t(($) => $.open.endAllTitle)}
        description={t(($) => $.open.endAllDescription)}
        confirm={t(($) => $.open.endAllConfirm)}
        finalFocus={dialogFocus}
        onConfirm={() => {
          returnTo.current = null;
          endAll();
        }}
      />
    </div>
  );
}

function EndSessionButton({
  label,
  disabled,
  heading,
  onClick,
}: {
  label: string;
  disabled: boolean;
  heading: RefObject<HTMLHeadingElement | null>;
  onClick: (event: MouseEvent<HTMLButtonElement>) => void;
}) {
  const { t } = useTranslation("troubleshooting");
  const button = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => {
    const node = button.current;
    const fallback = heading.current;
    // A poll can remove this row while its button holds focus. Layout cleanup
    // runs while the button is still in the document, so this sees that focus.
    return () => {
      if (node === document.activeElement) fallback?.focus();
    };
  }, [heading]);
  return (
    <Button
      ref={button}
      disabled={disabled}
      aria-label={label}
      onClick={onClick}
    >
      {t(($) => $.open.end)}
    </Button>
  );
}

function ConfirmEnd({
  open,
  onOpenChange,
  title,
  description,
  confirm,
  finalFocus,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirm: string;
  finalFocus: () => HTMLElement | null;
  onConfirm: () => void;
}) {
  const { t } = useTranslation("troubleshooting");
  return (
    <AlertDialogRoot open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent finalFocus={finalFocus}>
        <AlertDialogTitle>{title}</AlertDialogTitle>
        <AlertDialogDescription>{description}</AlertDialogDescription>
        <AlertDialogActions>
          <AlertDialogClose render={<Button>{t(($) => $.open.keep)}</Button>} />
          <Button
            variant="danger"
            onClick={() => {
              onOpenChange(false);
              onConfirm();
            }}
          >
            {confirm}
          </Button>
        </AlertDialogActions>
      </AlertDialogContent>
    </AlertDialogRoot>
  );
}
