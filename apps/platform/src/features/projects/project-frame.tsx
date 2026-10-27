import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useContext, useRef } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, type Schema } from "../../api/client";
import { projectOptions } from "../../api/projects";
import { Button } from "../../components/button";
import { Icon } from "../../components/icons";
import { Link } from "../../components/link";
import { ErrorToast } from "../../components/toast";
import { WorkspaceContext } from "../../workspace-context";
import * as s from "./projects.styles";

export function ProjectBreadcrumbs({
  workspaceId,
  project,
}: {
  workspaceId?: string;
  project?: Schema["Project"];
}) {
  const { t } = useTranslation("projects");
  return (
    <nav aria-label={t(($) => $.detail.breadcrumbs)}>
      <ol className={s.breadcrumbs}>
        <li>
          <Link
            to="/"
            search={{ workspace: workspaceId }}
            activeOptions={{ exact: true }}
            variant="plain"
            className={s.breadcrumbLink}
          >
            {t(($) => $.title)}
          </Link>
        </li>
        {project && (
          <li className={s.breadcrumbItem}>
            <Icon>
              <path d="m9 5 7 7-7 7" />
            </Icon>
            <Link
              to="/"
              search={{ workspace: workspaceId, project: project.id }}
              activeOptions={{ exact: true }}
              variant="plain"
              className={s.breadcrumbLink}
            >
              <bdi>{project.name}</bdi>
            </Link>
          </li>
        )}
      </ol>
    </nav>
  );
}

export function ProjectFrame({
  projectId,
  children,
}: {
  projectId: string;
  children: (
    project: Schema["Project"],
    workspace: Schema["Workspace"],
  ) => ReactNode;
}) {
  const workspace = useContext(WorkspaceContext);
  const { t } = useTranslation("projects");
  const common = useTranslation("common").t;
  const project = useQuery(projectOptions(workspace?.id ?? "", projectId));
  const frame = useRef<HTMLElement>(null);
  const unavailable =
    !workspace ||
    !projectId ||
    (project.error instanceof ApiError &&
      [400, 403, 404].includes(project.error.status));
  return (
    <section
      ref={frame}
      tabIndex={-1}
      aria-label={t(($) => $.detail.title)}
      className={s.page}
    >
      {!project.data || unavailable ? (
        <>
          <ProjectBreadcrumbs workspaceId={workspace?.id} />
          {unavailable ? (
            <div className={s.intro}>
              <h1 className={s.title}>{t(($) => $.detail.unavailableTitle)}</h1>
              <p className={s.muted}>{t(($) => $.detail.unavailable)}</p>
            </div>
          ) : project.isPending ? (
            <>
              <h1 className={s.title}>{t(($) => $.detail.title)}</h1>
              <p role="status">{t(($) => $.detail.loading)}</p>
            </>
          ) : (
            <div className={s.state} role="alert">
              <h1 className={s.title}>{t(($) => $.detail.loadFailed)}</h1>
              <p className={s.muted}>{t(($) => $.loadRecovery)}</p>
              <Button
                type="button"
                disabled={project.isFetching}
                onClick={() => {
                  frame.current?.focus();
                  void project.refetch();
                }}
              >
                {t(($) => $.retry)}
              </Button>
            </div>
          )}
        </>
      ) : (
        <>
          <ErrorToast
            error={project.error}
            message={common(($) => $.requestFailed)}
          />
          {children(project.data, workspace!)}
        </>
      )}
    </section>
  );
}
