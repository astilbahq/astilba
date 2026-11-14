import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { Schema } from "../../api/client";
import { environmentsOptions } from "../../api/environments";
import { Button } from "../../components/button";
import { ButtonLink } from "../../components/button-link";
import { Icon } from "../../components/icons";
import { Link } from "../../components/link";
import { ErrorToast } from "../../components/toast";
import { usePageTitle } from "../../i18n/use-page-title";
import * as shared from "../../styles";
import { ProjectBreadcrumbs, ProjectFrame } from "./project-frame";
import * as s from "./projects.styles";

export function ProjectDetail({
  projectId,
  environmentId,
  renderEnvironment,
}: {
  projectId: string;
  environmentId?: string;
  renderEnvironment: (
    environment: Schema["Environment"],
    workspace: Schema["Workspace"],
  ) => ReactNode;
}) {
  return (
    <ProjectFrame projectId={projectId}>
      {(project, workspace) => (
        <ProjectEnvironments
          project={project}
          workspace={workspace}
          environmentId={environmentId}
          renderEnvironment={renderEnvironment}
        />
      )}
    </ProjectFrame>
  );
}

function ProjectEnvironments({
  project,
  workspace,
  environmentId,
  renderEnvironment,
}: {
  project: Schema["Project"];
  workspace: Schema["Workspace"];
  environmentId?: string;
  renderEnvironment: (
    environment: Schema["Environment"],
    workspace: Schema["Workspace"],
  ) => ReactNode;
}) {
  const { t } = useTranslation("projects");
  const common = useTranslation("common").t;
  const envT = useTranslation("environment").t;
  const environments = useQuery(
    environmentsOptions(workspace.id, project.id, environmentId),
  );
  const content = useRef<HTMLElement>(null);
  const environment = environments.data?.find(
    (item) => item.id === environmentId,
  );
  const write = workspace.role !== "viewer";
  const active = environments.data?.filter((item) => item.state === "active");
  const retired = environments.data?.filter((item) => item.state !== "active");
  usePageTitle(
    environment
      ? envT(($) => $.pageTitle, {
          environment: environment.name,
          project: project.name,
        })
      : t(($) => $.detail.pageTitle, { project: project.name }),
  );
  return (
    <>
      <ProjectBreadcrumbs
        workspaceId={workspace.id}
        project={environmentId ? project : undefined}
      />
      {!environment && (
        <div className={s.intro}>
          <h1 className={s.title}>
            <bdi>{project.name}</bdi>
          </h1>
          <p className={s.muted}>
            <bdi>{workspace.name}</bdi>
          </p>
        </div>
      )}
      <ErrorToast
        error={environments.data ? environments.error : null}
        message={common(($) => $.requestFailed)}
      />
      <section
        ref={content}
        tabIndex={-1}
        aria-label={t(($) => $.environments.title)}
        className={s.recoveryTarget}
      >
        {environments.isPending ? (
          <p role="status" className={s.state}>
            {t(($) => $.environments.loading)}
          </p>
        ) : environments.isError && !environments.data ? (
          <div className={s.state} role="alert">
            <h2 className={s.heading}>{t(($) => $.environments.loadFailed)}</h2>
            <p className={s.muted}>{t(($) => $.loadRecovery)}</p>
            <Button
              type="button"
              disabled={environments.isFetching}
              onClick={() => {
                content.current?.focus();
                void environments.refetch();
              }}
            >
              {t(($) => $.retry)}
            </Button>
          </div>
        ) : environmentId ? (
          environment ? (
            renderEnvironment(environment, workspace)
          ) : (
            <div className={s.state}>
              <h2 className={s.heading}>
                {t(($) => $.environments.unavailableTitle)}
              </h2>
              <p className={s.muted}>{t(($) => $.environments.unavailable)}</p>
            </div>
          )
        ) : (
          <section className={s.environments}>
            <header className={s.header}>
              <h2 className={s.heading}>{t(($) => $.environments.title)}</h2>
              {write && (
                <ButtonLink
                  to="/environments/new"
                  search={{ workspace: workspace.id, project: project.id }}
                  variant="secondary"
                >
                  {t(($) => $.environments.new)}
                </ButtonLink>
              )}
            </header>
            {active?.length ? (
              <ul className={s.list}>
                {active.map((item) => (
                  <EnvironmentRow
                    key={item.id}
                    environment={item}
                    workspaceId={workspace.id}
                  />
                ))}
              </ul>
            ) : retired?.length ? (
              <p className={s.muted}>{t(($) => $.environments.allRetired)}</p>
            ) : (
              <div className={s.state}>
                <h3 className={s.heading}>
                  {write
                    ? t(($) => $.environments.emptyTitle)
                    : t(($) => $.environments.emptyViewerTitle)}
                </h3>
                <p className={s.muted}>
                  {t(($) => $.environments.emptyDescription)}
                </p>
              </div>
            )}
            {!!retired?.length && (
              <details className={shared.workspaceUtility}>
                <summary>
                  {t(($) => $.environments.retired, { total: retired.length })}
                </summary>
                <ul className={s.list}>
                  {retired.map((item) => (
                    <EnvironmentRow
                      key={item.id}
                      environment={item}
                      workspaceId={workspace.id}
                    />
                  ))}
                </ul>
              </details>
            )}
            {!write && (
              <p className={s.muted}>{t(($) => $.environments.readOnly)}</p>
            )}
          </section>
        )}
      </section>
    </>
  );
}

function EnvironmentRow({
  environment,
  workspaceId,
}: {
  environment: Schema["Environment"];
  workspaceId: string;
}) {
  const { t } = useTranslation("projects");
  return (
    <li>
      <Link
        to="/"
        search={{
          workspace: workspaceId,
          project: environment.projectId,
          environment: environment.id,
        }}
        variant="plain"
        className={s.row}
      >
        <div className={s.rowDetails}>
          <strong className={s.name}>
            <bdi>{environment.name}</bdi>
          </strong>
          <span className={s.metadata}>
            {environment.configurationOwner === "compose"
              ? t(($) => $.environments.compose)
              : t(($) => $.environments.visual)}
            {environment.purpose === "preview_template" && (
              <span>{t(($) => $.environments.previewTemplate)}</span>
            )}
            {environment.purpose === "preview" && (
              <span>{t(($) => $.environments.preview)}</span>
            )}
            {environment.state === "retired" && (
              <span>{t(($) => $.environments.retiredState)}</span>
            )}
            {environment.state === "retiring" && (
              <span>{t(($) => $.environments.retiringState)}</span>
            )}
            {environment.state === "purging" && (
              <span>{t(($) => $.environments.purgingState)}</span>
            )}
            {environment.state === "purged" && (
              <span>
                {environment.purgedWithoutHost
                  ? t(($) => $.environments.purgedWithoutHostState)
                  : t(($) => $.environments.purgedState)}
              </span>
            )}
          </span>
        </div>
        <span className={s.open}>
          {t(($) => $.environments.open)}
          <Icon>
            <path d="m9 5 7 7-7 7" />
          </Icon>
        </span>
      </Link>
    </li>
  );
}
