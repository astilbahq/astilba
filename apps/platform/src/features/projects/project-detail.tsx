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
  const environments = useQuery(
    environmentsOptions(workspace.id, project.id, environmentId),
  );
  const content = useRef<HTMLElement>(null);
  const environment = environments.data?.find(
    (item) => item.id === environmentId,
  );
  const write = workspace.role !== "viewer";
  usePageTitle(t(($) => $.detail.pageTitle, { project: project.name }));
  return (
    <>
      <ProjectBreadcrumbs
        workspaceId={workspace.id}
        project={environmentId ? project : undefined}
      />
      <div className={s.intro}>
        <h1 className={s.title}>
          <bdi>{project.name}</bdi>
        </h1>
        <p className={s.muted}>
          <bdi>{workspace.name}</bdi>
        </p>
      </div>
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
            {environments.data?.length ? (
              <ul className={s.list}>
                {environments.data.map((item) => (
                  <li key={item.id}>
                    <Link
                      to="/"
                      search={{
                        workspace: workspace.id,
                        project: project.id,
                        environment: item.id,
                      }}
                      variant="plain"
                      className={s.row}
                    >
                      <div className={s.rowDetails}>
                        <strong className={s.name}>
                          <bdi>{item.name}</bdi>
                        </strong>
                        <span className={s.metadata}>
                          {item.configurationOwner === "compose"
                            ? t(($) => $.environments.compose)
                            : t(($) => $.environments.visual)}
                          {item.purpose === "preview_template" && (
                            <span>
                              {t(($) => $.environments.previewTemplate)}
                            </span>
                          )}
                          {item.purpose === "preview" && (
                            <span>{t(($) => $.environments.preview)}</span>
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
                ))}
              </ul>
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
            {!write && (
              <p className={s.muted}>{t(($) => $.environments.readOnly)}</p>
            )}
          </section>
        )}
      </section>
    </>
  );
}
