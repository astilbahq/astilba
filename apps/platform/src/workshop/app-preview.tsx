import { useQuery } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useLocation,
  useNavigate,
  useSearch,
} from "@tanstack/react-router";
import { http, HttpResponse } from "msw/http";
import { useState } from "react";
import type { Schema } from "../api/client";
import { sessionOptions } from "../api/session";
import { CreateEnvironment } from "../features/projects/create-environment";
import { ProjectDetail } from "../features/projects/project-detail";
import {
  AppShell,
  type AppSection,
  WorkspaceUnavailable,
} from "../features/shell/app-shell";
import { Dashboard } from "../workspace";
import { WorkspaceContext } from "../workspace-context";
import { hostedMember } from "./fixtures";

function EnvironmentCreation() {
  const search = useSearch({ strict: false });
  const navigate = useNavigate();
  return (
    <CreateEnvironment
      projectId={search.project ?? ""}
      onCreated={(environment, workspaceId) =>
        void navigate({
          to: "/",
          search: {
            workspace: workspaceId,
            project: environment.projectId,
            environment: environment.id,
          },
        })
      }
    />
  );
}
export const workspace = hostedMember.workspaces[0];
export const session = {
  ...hostedMember,
  workspaces: [
    workspace,
    {
      ...workspace,
      id: "00000000-0000-4000-8000-000000000003",
      name: "Personal",
    },
  ],
};
export const examples: Schema["Project"][] = [
  {
    id: "00000000-0000-4000-8000-000000000010",
    createdAt: "2026-01-01T00:00:00Z",
    name: "Website",
  },
  {
    id: "00000000-0000-4000-8000-000000000011",
    createdAt: "2026-01-01T00:00:00Z",
    name: "Internal tools",
  },
];
export const projectsPath = "/api/v1/workspaces/:workspaceId/projects";
export const list = (projects = examples) =>
  http.get(projectsPath, () => HttpResponse.json(projects));
function PreviewLayout() {
  const current = useQuery(sessionOptions).data!;
  const search = useSearch({ strict: false });
  const location = useLocation();
  const navigate = useNavigate();
  const section = (
    location.pathname === "/environments/new" ? "/" : location.pathname
  ) as AppSection;
  const selected =
    search.workspace === undefined
      ? current.workspaces[0]
      : current.workspaces.find((item) => item.id === search.workspace);
  return (
    <WorkspaceContext value={selected}>
      <AppShell
        key={current.user!.id}
        user={current.user!}
        workspaces={current.workspaces}
        workspace={selected}
        selectedWorkspaceId={search.workspace}
        section={section}
        navigationKey={location.href}
        onWorkspaceChange={(id) =>
          void navigate({
            to: section,
            search: { workspace: id },
          })
        }
        onSignedOut={() => void navigate({ to: "/login" })}
      >
        <div
          key={
            location.pathname === "/account"
              ? "account"
              : (selected?.id ?? "none")
          }
        >
          {search.workspace !== undefined &&
          !selected &&
          location.pathname !== "/account" ? (
            <WorkspaceUnavailable />
          ) : (
            <Outlet />
          )}
        </div>
      </AppShell>
    </WorkspaceContext>
  );
}
function ProjectPage() {
  const selection = useSearch({ strict: false });
  return selection.project ? (
    <ProjectDetail
      projectId={selection.project}
      environmentId={selection.environment}
      renderEnvironment={(environment) => <h2>{environment.name}</h2>}
    />
  ) : (
    <Dashboard selection={selection} />
  );
}
export function ShellPreview({
  initialWorkspace,
  initialPath,
}: {
  initialWorkspace?: string;
  initialPath?: string;
}) {
  const [router] = useState(() => {
    const root = createRootRoute({
      component: PreviewLayout,
      validateSearch: (
        search: Record<string, unknown>,
      ): { workspace?: string; project?: string; environment?: string } => ({
        workspace:
          typeof search.workspace === "string" ? search.workspace : undefined,
        project:
          typeof search.project === "string" ? search.project : undefined,
        environment:
          typeof search.environment === "string"
            ? search.environment
            : undefined,
      }),
    });
    const index = createRoute({
      getParentRoute: () => root,
      path: "/",
      component: ProjectPage,
    });
    const creation = createRoute({
      getParentRoute: () => root,
      path: "/environments/new",
      component: EnvironmentCreation,
    });
    const children = (
      ["/servers", "/alerts", "/members", "/account", "/login"] as const
    ).map((path) =>
      createRoute({
        getParentRoute: () => root,
        path,
        component: () => <h1>{path.slice(1)}</h1>,
      }),
    );
    return createRouter({
      routeTree: root.addChildren([index, creation, ...children]),
      history: createMemoryHistory({
        initialEntries: [
          initialPath ??
            (initialWorkspace
              ? `/?${new URLSearchParams({ workspace: initialWorkspace })}`
              : "/"),
        ],
      }),
    });
  });
  return <RouterProvider router={router} />;
}

export const environmentsPath =
  "/api/v1/workspaces/:workspaceId/projects/:projectId/environments";
export const environments: Schema["Environment"][] = [
  {
    id: "00000000-0000-4000-8000-000000000020",
    projectId: examples[0].id,
    name: "Development",
    configurationOwner: "dashboard",
    purpose: "standard",
    draftRevisionId: "00000000-0000-4000-8000-000000000100",
    deploymentUnit: {
      id: "00000000-0000-4000-8000-000000000200",
      runtime: "docker",
    },
  },
  {
    id: "00000000-0000-4000-8000-000000000021",
    projectId: examples[0].id,
    name: "Production",
    configurationOwner: "compose",
    purpose: "standard",
    draftRevisionId: "00000000-0000-4000-8000-000000000101",
    deploymentUnit: {
      id: "00000000-0000-4000-8000-000000000201",
      runtime: "docker",
    },
  },
  {
    id: "00000000-0000-4000-8000-000000000022",
    projectId: examples[0].id,
    name: "Pull requests",
    configurationOwner: "dashboard",
    purpose: "preview_template",
    draftRevisionId: "00000000-0000-4000-8000-000000000102",
    deploymentUnit: {
      id: "00000000-0000-4000-8000-000000000202",
      runtime: "docker",
    },
  },
];
export const projectPath =
  "/api/v1/workspaces/:workspaceId/projects/:projectId";
export const projectHandlers = () => [
  list(),
  http.get(projectPath, ({ params }) => {
    const project = examples.find((item) => item.id === params.projectId);
    return project
      ? HttpResponse.json(project)
      : HttpResponse.json(
          { code: "not_found", message: "Unavailable", requestId: "workshop" },
          { status: 404 },
        );
  }),
  http.get(environmentsPath, () => HttpResponse.json(environments)),
];
