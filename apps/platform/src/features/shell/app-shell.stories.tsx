import type { Meta, StoryObj } from "@storybook/tanstack-react";
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
import { delay } from "msw";
import { http, HttpResponse } from "msw/http";
import { useState } from "react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { hostedMember, scenario } from "../../workshop/fixtures";
import { Dashboard } from "../../workspace";
import { WorkspaceContext } from "../../workspace-context";
import { AppShell, type AppSection, WorkspaceUnavailable } from "./app-shell";

const workspace = hostedMember.workspaces[0];
const session = {
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
const examples: Schema["Project"][] = [
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
const projectsPath = "/api/v1/workspaces/:workspaceId/projects";
const list = (projects = examples) =>
  http.get(projectsPath, () => HttpResponse.json(projects));
function PreviewLayout() {
  const current = useQuery(sessionOptions).data!;
  const search = useSearch({ strict: false });
  const location = useLocation();
  const navigate = useNavigate();
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
        section={location.pathname as AppSection}
        navigationKey={location.href}
        onWorkspaceChange={(id) =>
          void navigate({
            to: location.pathname as AppSection,
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
  const navigate = useNavigate();
  return selection.project ? (
    <h1>Project destination</h1>
  ) : (
    <Dashboard
      selection={selection}
      onSelectionChange={(search) => navigate({ to: "/", search })}
    />
  );
}
function ShellPreview({ initialWorkspace }: { initialWorkspace?: string }) {
  const [router] = useState(() => {
    const root = createRootRoute({
      component: PreviewLayout,
      validateSearch: (
        search: Record<string, unknown>,
      ): { workspace?: string; project?: string } => ({
        workspace:
          typeof search.workspace === "string" ? search.workspace : undefined,
        project:
          typeof search.project === "string" ? search.project : undefined,
      }),
    });
    const index = createRoute({
      getParentRoute: () => root,
      path: "/",
      component: ProjectPage,
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
      routeTree: root.addChildren([index, ...children]),
      history: createMemoryHistory({
        initialEntries: [
          initialWorkspace
            ? `/?${new URLSearchParams({ workspace: initialWorkspace })}`
            : "/",
        ],
      }),
    });
  });
  return <RouterProvider router={router} />;
}
const meta = {
  title: "Pages/Projects",
  component: ShellPreview,
  parameters: scenario(session, list()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Populated: Story = {};
export const Empty: Story = { parameters: scenario(session, list([])) };
export const Loading: Story = {
  parameters: scenario(
    session,
    http.get(projectsPath, async () => {
      await delay("infinite");
      return HttpResponse.json([]);
    }),
  ),
};
export const LoadFailure: Story = {
  parameters: scenario(
    session,
    http.get(projectsPath, () => HttpResponse.error()),
  ),
};
export const Viewer: Story = {
  parameters: scenario(
    { ...session, workspaces: [{ ...workspace, role: "viewer" }] },
    list(),
  ),
};
export const NoWorkspace: Story = {
  parameters: scenario({ ...session, workspaces: [] }),
};
export const LongNames: Story = {
  parameters: scenario(
    {
      ...session,
      user: {
        ...session.user!,
        name: "Alexandra Montgomery-Witherspoon",
        email: "alexandra.montgomery.witherspoon@example.test",
      },
      workspaces: [
        {
          ...workspace,
          name: "Infrastructure and operations for international services",
        },
      ],
    },
    list([
      {
        ...examples[0],
        name: "customer-facing-production-services-with-a-very-long-unbroken-name",
      },
    ]),
  ),
};
export const NewProject: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.click(
      await within(canvasElement).findByRole("button", { name: "New project" }),
    );
    await expect(
      within(canvasElement).getByLabelText("Project name", {}),
    ).toBeVisible();
  },
};
export const ValidationError: Story = {
  play: async (context) => {
    await NewProject.play!(context);
    const canvas = within(context.canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Create project" }),
    );
    await expect(
      canvas.getByLabelText("Project name", {}),
    ).toHaveAccessibleDescription("Enter a project name.");
    await expect(canvas.getByLabelText("Project name", {})).toHaveFocus();
  },
};
export const CreateFailure: Story = {
  parameters: scenario(
    session,
    list(),
    http.post(projectsPath, () => HttpResponse.error()),
  ),
  play: async (context) => {
    await NewProject.play!(context);
    const canvas = within(context.canvasElement);
    await userEvent.type(
      canvas.getByLabelText("Project name", {}),
      "API service",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Create project" }),
    );
    const toast = await within(
      context.canvasElement.ownerDocument.body,
    ).findByRole("dialog");
    await waitFor(() => expect(toast).toBeVisible(), { timeout: 5000 });
    await expect(canvas.getByLabelText("Project name", {})).toHaveValue(
      "API service",
    );
  },
};
let created = false;
export const Created: Story = {
  beforeEach: () => {
    created = false;
  },
  parameters: scenario(
    session,
    http.get(projectsPath, () =>
      HttpResponse.json(
        created
          ? [
              ...examples,
              {
                ...examples[0],
                id: "00000000-0000-4000-8000-000000000012",
                name: "API service",
              },
            ]
          : examples,
      ),
    ),
    http.post(projectsPath, () => {
      created = true;
      return HttpResponse.json(
        {
          ...examples[0],
          id: "00000000-0000-4000-8000-000000000012",
          name: "API service",
        },
        { status: 201 },
      );
    }),
  ),
  play: async (context) => {
    await NewProject.play!(context);
    const canvas = within(context.canvasElement);
    await userEvent.type(
      canvas.getByLabelText("Project name", {}),
      "API service",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Create project" }),
    );
    await expect(
      await canvas.findByRole("link", { name: "API service Open project" }),
    ).toBeVisible();
    await expect(canvas.getByRole("status")).toHaveTextContent(
      "Project created.",
    );
  },
};

export const UnavailableWorkspace: Story = {
  args: { initialWorkspace: "00000000-0000-4000-8000-000000000099" },
};
export const WorkspaceRecovered: Story = {
  ...UnavailableWorkspace,
  play: async ({ canvasElement }) => {
    await userEvent.click(
      await within(canvasElement).findByRole("link", {
        name: "Return to projects",
      }),
    );
    await expect(
      await within(canvasElement).findByRole("heading", { name: "Projects" }),
    ).toBeVisible();
  },
};
export const Submitting: Story = {
  parameters: scenario(
    session,
    list(),
    http.post(projectsPath, async () => {
      await delay("infinite");
      return HttpResponse.json(examples[0], { status: 201 });
    }),
  ),
  play: async (context) => {
    await NewProject.play!(context);
    const canvas = within(context.canvasElement);
    await userEvent.type(
      canvas.getByLabelText("Project name", {}),
      "API service",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Create project" }),
    );
    await expect(
      canvas.getByRole("button", { name: "Working…" }),
    ).toBeDisabled();
  },
};

export const WorkspaceChangeClearsDraft: Story = {
  play: async (context) => {
    await NewProject.play!(context);
    const canvas = within(context.canvasElement);
    await userEvent.type(
      canvas.getByLabelText("Project name", {}),
      "Workspace-specific draft",
    );
    const body = within(context.canvasElement.ownerDocument.body);
    if (!canvas.queryByRole("button", { name: "Workspace: My workspace" })) {
      await userEvent.click(
        canvas.getByRole("button", { name: "Open navigation" }),
      );
    }
    await userEvent.click(
      body.getByRole("button", { name: "Workspace: My workspace" }),
    );
    await userEvent.click(
      await within(context.canvasElement.ownerDocument.body).findByRole(
        "menuitem",
        { name: "Personal" },
      ),
    );
    await waitFor(() =>
      expect(canvas.queryByLabelText("Project name", {})).toBeNull(),
    );
    await expect(
      await canvas.findByRole("button", { name: "New project" }),
    ).toBeVisible();
  },
};

export const AccountIdentity: Story = {
  parameters: scenario(
    {
      ...session,
      user: {
        ...session.user!,
        name: "Riley Chen",
        email: "platform-team@example.test",
      },
    },
    list(),
  ),
};

export const CurrentWorkspaceKeepsProject: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("link", { name: "Website Open project" }),
    );
    await expect(
      await canvas.findByRole("heading", { name: "Project destination" }),
    ).toBeVisible();
    if (!canvas.queryByRole("button", { name: "Workspace: My workspace" })) {
      await userEvent.click(
        canvas.getByRole("button", { name: "Open navigation" }),
      );
    }
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      body.getByRole("button", { name: "Workspace: My workspace" }),
    );
    await userEvent.click(
      await body.findByRole("menuitem", { name: "My workspace" }),
    );
    const close = body.queryByRole("button", { name: "Close navigation" });
    if (close) await userEvent.click(close);
    await expect(
      canvas.getByRole("heading", { name: "Project destination" }),
    ).toBeVisible();
  },
};
