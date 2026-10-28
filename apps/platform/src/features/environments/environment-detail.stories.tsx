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
import {
  draftPath,
  environmentRoot,
  runtimePath,
  revision,
  observation,
} from "../../workshop/environment-fixtures";
import { scenario } from "../../workshop/fixtures";

const path = `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${environments[0].id}`;
const meta = {
  title: "Pages/Environment detail",
  component: ShellPreview,
  args: { initialPath: path },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Overview: Story = {};
export const Observed: Story = {
  parameters: scenario(
    session,
    http.get(runtimePath, () => HttpResponse.json(observation)),
    ...projectHandlers(),
  ),
};
export const Drift: Story = {
  parameters: scenario(
    session,
    http.get(runtimePath, () =>
      HttpResponse.json({
        ...observation,
        infrastructureDrift: true,
        omittedResources: 1,
      }),
    ),
    ...projectHandlers(),
  ),
};
export const Empty: Story = {
  parameters: scenario(
    session,
    http.get(draftPath, () =>
      HttpResponse.json({
        ...revision,
        specification: { version: 1, applications: [], variables: [] },
      }),
    ),
    ...projectHandlers(),
  ),
};
export const Loading: Story = {
  parameters: scenario(
    session,
    http.get(draftPath, async () => {
      await delay("infinite");
      return HttpResponse.json(revision);
    }),
    ...projectHandlers(),
  ),
};
let releaseEnvironmentList: () => void;
let environmentListGate: Promise<void>;
let environmentListAttempts = 0;
export const EnvironmentListRecovery: Story = {
  beforeEach: () => {
    environmentListAttempts = 0;
    environmentListGate = new Promise<void>((resolve) => {
      releaseEnvironmentList = resolve;
    });
    return () => releaseEnvironmentList();
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, async () => {
      if (environmentListAttempts++ > 0) return HttpResponse.json(environments);
      await environmentListGate;
      return HttpResponse.error();
    }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("Loading environments…");
    await expect(canvas.getByRole("heading", { level: 1 })).toHaveTextContent(
      "Website",
    );
    releaseEnvironmentList();
    await canvas.findByRole("heading", {
      name: "Environments could not be loaded",
    });
    await expect(canvas.getByRole("heading", { level: 1 })).toHaveTextContent(
      "Website",
    );
    await userEvent.click(canvas.getByRole("button", { name: "Try again" }));
    await expect(
      await canvas.findByRole("heading", { level: 1, name: "Development" }),
    ).toBeVisible();
  },
};
export const LoadFailure: Story = {
  parameters: scenario(
    session,
    http.get(draftPath, () => HttpResponse.error()),
    ...projectHandlers(),
  ),
};
export const Recovered: Story = {
  parameters: scenario(
    session,
    http.get(draftPath, () => HttpResponse.error(), { once: true }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Try again" }),
    );
    await expect(
      await canvas.findByRole("heading", { name: "Services" }),
    ).toBeVisible();
    await expect(
      canvas.getByRole("region", { name: "Environment content" }),
    ).toHaveFocus();
  },
};
export const SettingsDuringDraftFailure: Story = {
  parameters: scenario(
    session,
    http.get(draftPath, () => HttpResponse.error()),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("heading", {
      name: "Configuration could not be loaded",
    });
    await userEvent.click(canvas.getByRole("tab", { name: "Settings" }));
    await expect(
      await canvas.findByRole("heading", { name: "Secrets" }),
    ).toBeVisible();
    await expect(
      await canvas.findByRole("region", { name: "Environment automation" }),
    ).toBeVisible();
    await expect(canvas.getByLabelText("Secret name", {})).toBeVisible();
  },
};
export const ObservationUnavailable: Story = {
  parameters: scenario(
    session,
    http.get(runtimePath, () => HttpResponse.error()),
    ...projectHandlers(),
  ),
};
export const ObservationRecovered: Story = {
  parameters: scenario(
    session,
    http.get(runtimePath, () => HttpResponse.error(), { once: true }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Try again" }),
    );
    await canvas.findByText("No observations yet");
    await expect(
      canvas.getByRole("region", { name: "Deployment" }),
    ).toHaveFocus();
  },
};
export const Viewer: Story = {
  parameters: scenario(
    { ...session, workspaces: [{ ...workspace, role: "viewer" }] },
    ...projectHandlers(),
  ),
};
export const Compose: Story = {
  args: { initialPath: path.replace(environments[0].id, environments[1].id) },
};
export const PreviewTemplate: Story = {
  args: { initialPath: path.replace(environments[0].id, environments[2].id) },
};
export const ManagedPreview: Story = {
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json([{ ...environments[0], purpose: "preview" }]),
    ),
    ...projectHandlers(),
  ),
};
export const LongNames: Story = {
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json([
        {
          ...environments[0],
          name: "production-services-for-customers-in-multiple-regions",
        },
      ]),
    ),
    http.get(draftPath, () =>
      HttpResponse.json({
        ...revision,
        specification: {
          ...revision.specification,
          applications: [
            {
              ...revision.specification.applications[0],
              name: "customer-facing-international-application-service",
              source: {
                kind: "image",
                image:
                  "registry.example.com/infrastructure/customer-facing-api:release-candidate",
              },
            },
          ],
        },
      }),
    ),
    ...projectHandlers(),
  ),
};
export const Configuration: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Open configuration" }),
    );
    await expect(
      canvas.getByRole("tab", { name: "Configuration" }),
    ).toHaveFocus();
    await expect(
      await canvas.findByRole("button", { name: "Save revision" }),
    ).toBeDisabled();
  },
};
const editedDraft = JSON.stringify(
  {
    version: 1,
    applications: [],
    variables: [{ name: "MODE", value: "development" }],
  },
  null,
  2,
);
let releaseRollback: () => void;
let rollbackGate: Promise<void>;
let rollbackStarted = false;
export const RollbackAfterEditing: Story = {
  beforeEach: () => {
    rollbackStarted = false;
    rollbackGate = new Promise<void>((resolve) => {
      releaseRollback = resolve;
    });
    return () => releaseRollback();
  },
  parameters: scenario(
    session,
    http.get(`${environmentRoot}/releases`, () =>
      HttpResponse.json([
        {
          planId: "00000000-0000-4000-8000-000000000400",
          revisionId: revision.id,
          createdAt: revision.createdAt,
          applications: [
            {
              id: revision.specification.applications[0].id,
              name: "api",
              image: "nginx:alpine",
              eligible: true,
              note: "",
              changes: [],
            },
          ],
        },
      ]),
    ),
    http.post(`${environmentRoot}/releases/:planId/rollback`, async () => {
      rollbackStarted = true;
      await rollbackGate;
      return HttpResponse.json({
        ...revision,
        id: "00000000-0000-4000-8000-000000000401",
        specification: {
          ...revision.specification,
          variables: [{ name: "MODE", value: "rollback" }],
        },
      });
    }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const deployments = await canvas.findByRole("tab", {
      name: "Deployments",
    });
    await userEvent.click(deployments);
    await userEvent.click(await canvas.findByText(/^Revision 00000000/));
    await userEvent.click(
      canvas.getByRole("button", { name: "Create rollback draft for api" }),
    );
    await waitFor(() => expect(rollbackStarted).toBe(true));
    await userEvent.click(canvas.getByRole("tab", { name: "Configuration" }));
    await userEvent.click(
      canvas.getByText("Advanced specification", { exact: true }),
    );
    const editor = canvas.getByLabelText("Specification (JSON)", {});
    await userEvent.clear(editor);
    await userEvent.click(editor);
    await userEvent.paste(editedDraft);
    await userEvent.click(deployments);
    releaseRollback();
    const notice = await canvas.findByText(
      "Another revision is ready to compare. Your edits are preserved. Open Configuration to review and merge them before saving.",
    );
    await expect(notice.closest('[aria-live="polite"]')).toBeVisible();
    await expect(deployments).toHaveAttribute("aria-selected", "true");
    await expect(deployments).toHaveFocus();
    await userEvent.click(
      canvas.getByRole("button", { name: "Open configuration" }),
    );
    await expect(editor).toHaveValue(editedDraft);
    const conflict = canvas.getByRole("region", {
      name: "Conflicting revision",
    });
    await expect(conflict).toBeVisible();
    await expect(conflict).toHaveTextContent('"value": "rollback"');
    await userEvent.click(
      within(conflict).getByRole("button", {
        name: "I have compared and merged my edits",
      }),
    );
    await expect(editor).toHaveValue(editedDraft);
    await expect(
      canvas.getByRole("button", { name: "Save revision" }),
    ).toBeEnabled();
  },
};
async function edit(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.click(
    await canvas.findByRole("button", { name: "Open configuration" }),
  );
  await userEvent.click(
    canvas.getByText("Advanced specification", { exact: true }),
  );
  const input = canvas.getByLabelText("Specification (JSON)", {});
  await userEvent.clear(input);
  await userEvent.click(input);
  await userEvent.paste(editedDraft);
  return canvas;
}
export const Unsaved: Story = {
  play: async ({ canvasElement }) => {
    const canvas = await edit(canvasElement);
    await userEvent.click(canvas.getByRole("tab", { name: "Overview" }));
    await expect(canvas.getByRole("status")).toHaveTextContent(
      "You have unsaved configuration or layout changes.",
    );
  },
};
export const SaveFailure: Story = {
  parameters: scenario(
    session,
    http.put(draftPath, () => HttpResponse.error()),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await edit(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Save revision" }),
    );
    await expect(await canvas.findByRole("alert")).toBeVisible();
    await expect(canvas.getByLabelText("Specification (JSON)", {})).toHaveValue(
      editedDraft,
    );
  },
};
export const Conflict: Story = {
  parameters: scenario(
    session,
    http.put(draftPath, () =>
      HttpResponse.json(
        {
          code: "conflict",
          message: "Another revision was saved",
          requestId: "workshop",
          currentRevision: {
            ...revision,
            id: "00000000-0000-4000-8000-000000000110",
          },
        },
        { status: 409 },
      ),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await edit(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Save revision" }),
    );
    await expect(
      await canvas.findByRole("heading", {
        name: "Another revision was saved",
      }),
    ).toBeVisible();
    await expect(canvas.getByLabelText("Specification (JSON)", {})).toHaveValue(
      editedDraft,
    );
  },
};
export const Deployments: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const button = await canvas.findByRole("button", {
      name: "Review and deploy",
    });
    await canvas.findByRole("heading", { name: "Services" });
    await userEvent.click(button);
    await expect(
      await canvas.findByRole("heading", { name: "Review and deploy" }),
    ).toBeVisible();
  },
};
export const Settings: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("tab", { name: "Settings" }));
    await expect(
      await canvas.findByRole("heading", { name: "Secrets" }),
    ).toBeVisible();
  },
};
