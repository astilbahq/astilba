import { useQueries, useQuery } from "@tanstack/react-query";
import { useRef } from "react";
import { useTranslation } from "react-i18next";
import type { Schema } from "../../api/client";
import { environmentsOptions } from "../../api/environments";
import { projectsOptions } from "../../api/projects";
import * as s from "../../styles";
import { historyOptions } from "./session-queries";
import {
  SessionLoadFailed,
  shortId,
  useSessionNames,
  useSessionText,
} from "./session-text";
import * as styles from "./troubleshooting.styles";

export function SessionHistory({ workspaceId }: { workspaceId: string }) {
  const { t } = useTranslation("troubleshooting");
  const history = useQuery(historyOptions(workspaceId));
  const heading = useRef<HTMLHeadingElement>(null);
  return (
    <section className={s.panel} aria-label={t(($) => $.history.title)}>
      <div className={styles.intro}>
        <h2 ref={heading} tabIndex={-1} className={styles.focusTarget}>
          {t(($) => $.history.title)}
        </h2>
        <p className={s.muted}>{t(($) => $.history.description)}</p>
      </div>
      {history.isPending ? (
        <p role="status">{t(($) => $.history.loading)}</p>
      ) : !history.data ? (
        <SessionLoadFailed
          message={t(($) => $.history.loadFailed)}
          heading={heading}
          retry={() => void history.refetch()}
        />
      ) : history.data.length === 0 ? (
        <p>{t(($) => $.history.empty)}</p>
      ) : (
        <HistoryTable workspaceId={workspaceId} sessions={history.data} />
      )}
    </section>
  );
}

function HistoryTable({
  workspaceId,
  sessions,
}: {
  workspaceId: string;
  sessions: Schema["TroubleshootingSession"][];
}) {
  const { t } = useTranslation("troubleshooting");
  const text = useSessionText();
  const region = t(($) => $.history.region);
  const projects = useQuery(projectsOptions(workspaceId)).data ?? [];
  const environments = useQueries({
    queries: projects.map((project) =>
      environmentsOptions(workspaceId, project.id),
    ),
    combine: (lists) => lists.flatMap((list) => list.data ?? []),
  });
  const sessionEnvironments = environments.filter((environment) =>
    sessions.some((session) => session.environmentId === environment.id),
  );
  const names = useSessionNames(
    workspaceId,
    true,
    sessionEnvironments.map((environment) => ({
      workspaceId,
      projectId: environment.projectId,
      environmentId: environment.id,
    })),
  );
  return (
    // oxlint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- WebKit needs focus to keyboard-scroll a clipped table.
    <section className={styles.historyTable} tabIndex={0} aria-label={region}>
      <table>
        <caption className={s.screenReaderOnly}>
          {t(($) => $.history.caption)}
        </caption>
        <thead>
          <tr>
            <th scope="col">{t(($) => $.history.target)}</th>
            <th scope="col">{t(($) => $.history.person)}</th>
            <th scope="col">{t(($) => $.history.start)}</th>
            <th scope="col">{t(($) => $.history.duration)}</th>
            <th scope="col">{t(($) => $.history.state)}</th>
            <th scope="col">{t(($) => $.history.closeReason)}</th>
          </tr>
        </thead>
        <tbody>
          {sessions.map((session) => (
            <tr key={session.id}>
              <th scope="row">
                <bdi className={styles.targetName}>{names.target(session)}</bdi>
                <span className={styles.metadata}>
                  <bdi>
                    {environments.find(({ id }) => id === session.environmentId)
                      ?.name ??
                      t(($) => $.fallback.environment, {
                        id: shortId(session.environmentId),
                      })}
                  </bdi>
                  <bdi>{names.server(session)}</bdi>
                </span>
              </th>
              <td>
                <bdi>{names.person(session)}</bdi>
              </td>
              <td className={styles.nowrap}>
                <time dateTime={session.createdAt}>
                  {text.date(session.createdAt)}
                </time>
              </td>
              <td className={styles.nowrap}>{text.duration(session)}</td>
              <td className={styles.nowrap}>{text.state(session)}</td>
              <td>{text.closeReason(session.closeReason)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
