import { useQuery } from "@tanstack/react-query";
import { client, result } from "./api/client";
import { EnvironmentDetail } from "./features/environments/environment-detail";
import { ProjectDetail } from "./features/projects/project-detail";
import { ProjectsOverview } from "./features/projects/projects-overview";
import { hostingOptions } from "./hosting";
import { WorkspaceImport } from "./portability";
import * as s from "./styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";
export type WorkspaceSelection = {
  workspace?: string;
  project?: string;
  environment?: string;
};

export function Dashboard({ selection }: { selection: WorkspaceSelection }) {
  if (selection.project) {
    return (
      <ProjectDetail
        key={selection.project}
        projectId={selection.project}
        environmentId={selection.environment}
        renderEnvironment={(environment, workspace) => (
          <EnvironmentDetail
            key={environment.id}
            workspaceId={workspace.id}
            projectId={environment.projectId}
            environment={environment}
            write={workspace.role !== "viewer"}
            owner={workspace.role === "owner"}
          />
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
