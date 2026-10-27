import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { delay } from "msw";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import {
  ShellPreview,
  session,
  workspace,
  examples,
  environments,
  environmentsPath,
  projectHandlers,
} from "../../workshop/app-preview";
import { scenario } from "../../workshop/fixtures";

const path = `/environments/new?workspace=${workspace.id}&project=${examples[0].id}`;
const meta = {
  title: "Pages/New environment",
  component: ShellPreview,
  args: { initialPath: path },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Visual: Story = {};
export const Compose: Story = {
  play: async ({ canvasElement }) =>
    userEvent.selectOptions(
      await within(canvasElement).findByLabelText("Configuration", {}),
      "compose",
    ),
};
export const PreviewTemplate: Story = {
  play: async ({ canvasElement }) =>
    userEvent.selectOptions(
      await within(canvasElement).findByLabelText("Configuration", {}),
      "preview_template",
    ),
};
export const ValidationError: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Create environment" }),
    );
    const input = canvas.getByLabelText("Environment name", {});
    await expect(input).toHaveAccessibleDescription(
      "Enter an environment name.",
    );
    await expect(input).toHaveFocus();
  },
};
async function submit(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.type(
    await canvas.findByLabelText("Environment name", {}),
    "Staging",
  );
  await userEvent.click(
    canvas.getByRole("button", { name: "Create environment" }),
  );
}
export const RequestFailure: Story = {
  parameters: scenario(
    session,
    http.post(environmentsPath, () => HttpResponse.error()),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    await submit(canvasElement);
    const canvas = within(canvasElement);
    await expect(
      await within(canvasElement.ownerDocument.body).findByRole("dialog"),
    ).toHaveAccessibleDescription(
      "The request could not be completed. Try again.",
    );
    await expect(canvas.getByLabelText("Environment name", {})).toHaveValue(
      "Staging",
    );
  },
};
export const PermissionChanged: Story = {
  parameters: scenario(
    session,
    http.post(environmentsPath, () =>
      HttpResponse.json(
        { code: "forbidden", message: "Forbidden", requestId: "workshop" },
        { status: 403 },
      ),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    await submit(canvasElement);
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "You no longer have permission to create environments in this workspace.",
    );
    await expect(canvas.getByLabelText("Environment name", {})).toHaveValue(
      "Staging",
    );
  },
};
export const Viewer: Story = {
  parameters: scenario(
    { ...session, workspaces: [{ ...workspace, role: "viewer" }] },
    ...projectHandlers(),
  ),
};
export const Submitting: Story = {
  parameters: scenario(
    session,
    http.post(environmentsPath, async () => {
      await delay("infinite");
      return HttpResponse.json(environments[0]);
    }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    await submit(canvasElement);
    await expect(
      within(canvasElement).getByRole("button", { name: "Working…" }),
    ).toBeDisabled();
  },
};
const createdEnvironment = {
  ...environments[0],
  id: "00000000-0000-4000-8000-000000000023",
  name: "Staging",
};
let created = false;
export const Created: Story = {
  beforeEach: () => {
    created = false;
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(
        created ? [...environments, createdEnvironment] : environments,
      ),
    ),
    http.post(environmentsPath, () => {
      created = true;
      return HttpResponse.json(createdEnvironment, { status: 201 });
    }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    await submit(canvasElement);
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByRole("heading", { name: "Staging" }),
    ).toBeVisible();
    await expect(canvas.queryByLabelText("Environment name", {})).toBeNull();
  },
};
export const MissingProject: Story = {
  args: { initialPath: `/environments/new?workspace=${workspace.id}` },
};
export const WorkspaceChangeClearsDraft: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByLabelText("Environment name", {}),
      "Workspace-specific draft",
    );
    if (!canvas.queryByRole("button", { name: "Workspace: My workspace" }))
      await userEvent.click(
        canvas.getByRole("button", { name: "Open navigation" }),
      );
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      body.getByRole("button", { name: "Workspace: My workspace" }),
    );
    await userEvent.click(
      await body.findByRole("menuitem", { name: "Personal" }),
    );
    await waitFor(() =>
      expect(canvas.queryByLabelText("Environment name", {})).toBeNull(),
    );
    await expect(
      await canvas.findByRole("heading", { name: "Projects" }),
    ).toBeVisible();
  },
};
