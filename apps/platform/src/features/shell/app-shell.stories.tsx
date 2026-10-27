import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { delay } from "msw";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import {
  ShellPreview,
  examples,
  list,
  projectsPath,
  session,
  workspace,
  projectHandlers,
} from "../../workshop/app-preview";
import { scenario } from "../../workshop/fixtures";

const meta = {
  title: "Pages/Projects",
  component: ShellPreview,
  parameters: scenario(session, ...projectHandlers()),
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
      await canvas.findByRole("heading", { name: "Website" }),
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
      canvas.getByRole("heading", { name: "Website" }),
    ).toBeVisible();
  },
};
