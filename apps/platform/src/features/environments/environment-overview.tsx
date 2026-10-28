import { useQuery } from "@tanstack/react-query";
import { useRef } from "react";
import { useTranslation } from "react-i18next";
import type { Schema } from "../../api/client";
import { runtimeOptions, type EnvironmentPath } from "../../api/environment";
import { Button } from "../../components/button";
import { Icon } from "../../components/icons";
import * as s from "./environment.styles";

export function EnvironmentOverview({
  environment,
  path,
  revision,
  dirty,
  onConfigure,
  write,
}: {
  environment: Schema["Environment"];
  path: EnvironmentPath;
  revision: Schema["Revision"];
  dirty: boolean;
  onConfigure: () => void;
  write: boolean;
}) {
  const recovery = useRef<HTMLElement>(null);
  const { t, i18n } = useTranslation("environment");
  const template = environment.purpose === "preview_template";
  const runtime = useQuery({ ...runtimeOptions(path), enabled: !template });
  const observedAt = runtime.data?.observedAt;
  const observed = !!observedAt && Date.parse(observedAt) > 0;
  const applications = revision.specification.applications;
  return (
    <div className={s.overview}>
      <div className={s.summary}>
        <section
          className={s.summarySection}
          aria-label={t(($) => $.configuration)}
        >
          <h2 className={s.heading}>{t(($) => $.configuration)}</h2>
          <dl className={s.facts}>
            <dt>{t(($) => $.savedRevision)}</dt>
            <dd className={s.code}>{revision.hash.slice(0, 12)}</dd>
            <dt>{t(($) => $.services)}</dt>
            <dd>{t(($) => $.serviceCount, { count: applications.length })}</dd>
            <dt>{t(($) => $.editing)}</dt>
            <dd>
              {environment.configurationOwner === "compose"
                ? t(($) => $.compose)
                : t(($) => $.visual)}
            </dd>
          </dl>
          {dirty && (
            <p className={s.note} role="status">
              {t(($) => $.unsaved)}
            </p>
          )}
        </section>
        <section
          ref={recovery}
          tabIndex={-1}
          className={s.summarySection}
          aria-label={t(($) => $.deployment)}
        >
          <h2 className={s.heading}>{t(($) => $.deployment)}</h2>
          <dl className={s.facts}>
            <dt>{t(($) => $.server)}</dt>
            <dd>
              {environment.deploymentUnit.serverId
                ? t(($) => $.assigned)
                : t(($) => $.notAssigned)}
            </dd>
            <dt>{t(($) => $.runtime)}</dt>
            <dd>
              {template ? (
                t(($) => $.notDeployable)
              ) : runtime.isError && !runtime.data ? (
                t(($) => $.observationUnavailable)
              ) : runtime.isPending ? (
                t(($) => $.observationLoading)
              ) : observed ? (
                <time dateTime={observedAt}>
                  {t(($) => $.lastObserved, {
                    date: new Date(observedAt).toLocaleString(
                      i18n.resolvedLanguage,
                    ),
                  })}
                </time>
              ) : (
                t(($) => $.noObservations)
              )}
            </dd>
          </dl>
          {!template && runtime.isError && (
            <div>
              <p className={s.muted}>{t(($) => $.observationFailed)}</p>
              <Button
                disabled={runtime.isFetching}
                onClick={() => {
                  recovery.current?.focus();
                  void runtime.refetch();
                }}
              >
                {t(($) => $.retry)}
              </Button>
            </div>
          )}
          {!template && <p className={s.note}>{t(($) => $.deploymentNote)}</p>}
          {runtime.data?.infrastructureDrift && (
            <p className={s.note}>{t(($) => $.drift)}</p>
          )}
          {!!runtime.data?.omittedResources && (
            <p className={s.muted}>
              {t(($) => $.omitted, { count: runtime.data.omittedResources })}
            </p>
          )}
        </section>
      </div>
      <section aria-label={t(($) => $.services)}>
        <header className={s.sectionHeader}>
          <h2 className={s.heading}>{t(($) => $.services)}</h2>
          <Button onClick={onConfigure}>{t(($) => $.openConfiguration)}</Button>
        </header>
        <p className={s.muted}>{t(($) => $.savedServices)}</p>
        {applications.length ? (
          <ul className={s.list}>
            {applications.map((application) => {
              const observation = observed
                ? runtime.data?.resources.find(
                    (item) => item.id === application.id,
                  )
                : undefined;
              return (
                <li key={application.id} className={s.service}>
                  <span className={s.serviceName}>
                    <Icon>
                      <rect x="4" y="5" width="16" height="14" rx="2" />
                      <path d="m8 10 3 2-3 2m5 0h3" />
                    </Icon>
                    <bdi>{application.name}</bdi>
                  </span>
                  <span className={s.muted}>
                    <bdi>
                      {application.source.kind === "git"
                        ? application.source.repository
                        : application.source.image}
                    </bdi>
                  </span>
                  <span className={s.muted}>
                    {template
                      ? t(($) => $.templateService)
                      : observation
                        ? t(($) => $.observedState, {
                            state: observation.state,
                          })
                        : t(($) => $.notObserved)}
                  </span>
                </li>
              );
            })}
          </ul>
        ) : (
          <div className={s.state}>
            <h3 className={s.heading}>{t(($) => $.emptyTitle)}</h3>
            <p className={s.muted}>
              {!write
                ? t(($) => $.emptyReadOnly)
                : environment.configurationOwner === "compose"
                  ? t(($) => $.emptyCompose)
                  : t(($) => $.emptyDescription)}
            </p>
          </div>
        )}
      </section>
    </div>
  );
}
