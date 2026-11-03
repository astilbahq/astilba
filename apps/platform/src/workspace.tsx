import { useQuery } from "@tanstack/react-query";
import { CatchBoundary, lazyRouteComponent } from "@tanstack/react-router";
import { Suspense } from "react";
import { useTranslation } from "react-i18next";
import { client, result } from "./api/client";
import { Button } from "./components/button";
import { title as environmentTitle } from "./features/environments/environment.styles";
import { ProjectDetail } from "./features/projects/project-detail";
import { ProjectsOverview } from "./features/projects/projects-overview";
import { hostingOptions } from "./hosting";
import { WorkspaceImport } from "./portability";
import * as s from "./styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";

const EnvironmentDetail = lazyRouteComponent(
  () => import("./features/environments/environment-detail"),
  "EnvironmentDetail",
);

function EnvironmentLoadError() {
  const { t } = useTranslation("environment");
  const common = useTranslation("common").t;
  return (
    <div className={s.stack}>
      <div role="alert">
        <h1 className={environmentTitle}>{t(($) => $.viewLoadFailed)}</h1>
      </div>
      <div>
        <Button variant="secondary" onClick={() => window.location.reload()}>
          {common(($) => $.reloadPage)}
        </Button>
      </div>
    </div>
  );
}
export type WorkspaceSelection = {
  workspace?: string;
  project?: string;
  environment?: string;
};

export function Dashboard({ selection }: { selection: WorkspaceSelection }) {
  const { t } = useTranslation("common");
  if (selection.project) {
    return (
      <ProjectDetail
        key={selection.project}
        projectId={selection.project}
        environmentId={selection.environment}
        renderEnvironment={(environment, workspace) => (
          <CatchBoundary
            getResetKey={() => environment.id}
            errorComponent={EnvironmentLoadError}
          >
            <Suspense
              fallback={
                <div className={s.stack}>
                  <h1 className={environmentTitle}>
                    <bdi>{environment.name}</bdi>
                  </h1>
                  <p role="status" className={s.muted}>
                    {t(($) => $.loading)}
                  </p>
                </div>
              }
            >
              <EnvironmentDetail
                key={environment.id}
                workspaceId={workspace.id}
                projectId={environment.projectId}
                environment={environment}
                write={workspace.role !== "viewer"}
                owner={workspace.role === "owner"}
              />
            </Suspense>
          </CatchBoundary>
        )}
      />
    );
  }
  return <ProjectsLanding />;
}
function ProjectsLanding() {
  const action = useAction();
  const hosted = useQuery(hostingOptions).data?.enabled;
  return (
    <div className={s.stack}>
      <ErrorNotice error={action.error} />
      <ProjectsOverview />
      <WorkspaceImport hosted={!!hosted} />
      <details className={s.workspaceUtility}>
        <summary>Create another workspace</summary>
        <Form
          action={action}
          onSubmit={(data, form) =>
            action.run(async () => {
              await result(
                client.POST("/api/v1/workspaces", {
                  body: {
                    name: text(data, "name"),
                    invitation: hosted ? text(data, "invitation") : undefined,
                  },
                }),
              );
              form.reset();
            })
          }
        >
          <Field name="name" label="Workspace name" />
          {hosted && (
            <Field
              name="invitation"
              label="Beta invitation code"
              autoComplete="off"
            />
          )}
          <BusyButton busy={action.isPending}>Create workspace</BusyButton>
        </Form>
      </details>
    </div>
  );
}
