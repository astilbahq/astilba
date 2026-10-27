import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { delay } from "msw";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, within } from "storybook/test";
import {
  ShellPreview,
  session,
  workspace,
  examples,
  environments,
  environmentsPath,
  projectPath,
  projectHandlers,
} from "../../workshop/app-preview";
import { scenario } from "../../workshop/fixtures";

const path = `/?workspace=${workspace.id}&project=${examples[0].id}`;
const meta = {
  title: "Pages/Project detail",
  component: ShellPreview,
  args: { initialPath: path },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Populated: Story = {};
export const Empty: Story = {
  parameters: scenario(
    session,
    http.get(environmentsPath, () => HttpResponse.json([])),
    ...projectHandlers(),
  ),
};
export const Loading: Story = {
  parameters: scenario(
    session,
    http.get(environmentsPath, async () => {
      await delay("infinite");
      return HttpResponse.json([]);
    }),
    ...projectHandlers(),
  ),
};
export const LoadFailure: Story = {
  parameters: scenario(
    session,
    http.get(environmentsPath, () => HttpResponse.error()),
    ...projectHandlers(),
  ),
};
export const Viewer: Story = {
  parameters: scenario(
    { ...session, workspaces: [{ ...workspace, role: "viewer" }] },
    ...projectHandlers(),
  ),
};
export const EmptyViewer: Story = {
  parameters: scenario(
    { ...session, workspaces: [{ ...workspace, role: "viewer" }] },
    http.get(environmentsPath, () => HttpResponse.json([])),
    ...projectHandlers(),
  ),
};
export const ProjectLoading: Story = {
  parameters: scenario(
    session,
    http.get(projectPath, async () => {
      await delay("infinite");
      return HttpResponse.json(examples[0]);
    }),
    ...projectHandlers(),
  ),
};
export const ProjectLoadFailure: Story = {
  parameters: scenario(
    session,
    http.get(projectPath, () => HttpResponse.error()),
    ...projectHandlers(),
  ),
};
export const ProjectRecovered: Story = {
  parameters: scenario(
    session,
    http.get(projectPath, () => HttpResponse.error(), { once: true }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const retry = await canvas.findByRole("button", { name: "Try again" });
    retry.focus();
    await userEvent.keyboard("{Enter}");
    await expect(
      await canvas.findByRole("heading", { name: "Website" }),
    ).toBeVisible();
    await expect(canvas.getByRole("region", { name: "Project" })).toHaveFocus();
  },
};
export const EnvironmentsRecovered: Story = {
  parameters: scenario(
    session,
    http.get(environmentsPath, () => HttpResponse.error(), { once: true }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const retry = await canvas.findByRole("button", { name: "Try again" });
    retry.focus();
    await userEvent.keyboard("{Enter}");
    await expect(
      await canvas.findByRole("link", {
        name: /Development.*Open environment/,
      }),
    ).toBeVisible();
    await expect(
      canvas.getByRole("region", { name: "Environments" }),
    ).toHaveFocus();
    await userEvent.tab();
    await expect(
      canvas.getByRole("link", { name: "New environment" }),
    ).toHaveFocus();
  },
};
export const ProjectUnavailable: Story = {
  args: {
    initialPath: `/?workspace=${workspace.id}&project=00000000-0000-4000-8000-000000000099`,
  },
};
export const EnvironmentUnavailable: Story = {
  args: {
    initialPath: `${path}&environment=00000000-0000-4000-8000-000000000099`,
  },
};
export const LongNames: Story = {
  parameters: scenario(
    {
      ...session,
      workspaces: [
        {
          ...workspace,
          name: "Infrastructure and operations for international services",
        },
      ],
    },
    http.get(projectPath, () =>
      HttpResponse.json({
        ...examples[0],
        name: "customer-facing-international-production-services",
      }),
    ),
    http.get(environmentsPath, () =>
      HttpResponse.json([
        {
          ...environments[0],
          name: "production-services-for-customers-in-multiple-regions",
        },
      ]),
    ),
    ...projectHandlers(),
  ),
};
export const OpenEnvironment: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("link", {
        name: /Development.*Open environment/,
      }),
    );
    await expect(
      await canvas.findByRole("heading", { name: "Development" }),
    ).toBeVisible();
    const breadcrumb = canvas.getByRole("navigation", { name: "Breadcrumb" });
    for (const link of within(breadcrumb).getAllByRole("link"))
      await expect(link).not.toHaveAttribute("aria-current", "page");
  },
};
export const NewEnvironment: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("link", { name: "New environment" }),
    );
    await expect(
      await canvas.findByRole("heading", { name: "New environment" }),
    ).toBeVisible();
    await expect(canvas.getByLabelText("Environment name", {})).toBeVisible();
  },
};
export const CancelCreation: Story = {
  play: async (context) => {
    await NewEnvironment.play!(context);
    const canvas = within(context.canvasElement);
    await userEvent.type(
      canvas.getByLabelText("Environment name", {}),
      "Unsubmitted draft",
    );
    await userEvent.click(canvas.getByRole("link", { name: "Cancel" }));
    await expect(
      await canvas.findByRole("heading", { name: "Website" }),
    ).toBeVisible();
    await expect(canvas.queryByLabelText("Environment name", {})).toBeNull();
  },
};
