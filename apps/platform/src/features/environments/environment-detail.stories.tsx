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
  composeRevision,
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

async function configure(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.click(
    await canvas.findByRole("button", { name: "Open configuration" }),
  );
  return canvas;
}
export const ServiceList: Story = {
  play: async ({ canvasElement }) => {
    const canvas = await configure(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Service list" }));
  },
};
export const SelectedService: Story = {
  play: async ({ canvasElement }) => {
    const canvas = await configure(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Service list" }));
    await userEvent.click(canvas.getByRole("button", { name: "api" }));
  },
};
export const UnsavedLayout: Story = {
  play: async ({ canvasElement }) => {
    const canvas = await configure(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Service list" }));
    await userEvent.click(canvas.getByRole("button", { name: "api" }));
    const input = canvas.getByLabelText("Position x", { exact: true });
    await userEvent.clear(input);
    await userEvent.type(input, "25");
    await userEvent.tab();
    await expect(
      canvas.getByRole("button", { name: "Save canvas layout" }),
    ).toBeEnabled();
    await expect(
      canvas.getByRole("button", { name: "Save revision" }),
    ).toBeDisabled();
  },
};
export const InvalidSpecification: Story = {
  play: async ({ canvasElement }) => {
    const canvas = await edit(canvasElement);
    const input = canvas.getByLabelText("Specification (JSON)", {});
    await userEvent.clear(input);
    await userEvent.type(input, "invalid JSON");
    await userEvent.click(
      canvas.getByText("Advanced specification", { exact: true }),
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Save revision" }),
    );
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "Enter valid JSON before saving.",
    );
    await waitFor(() => expect(input).toHaveFocus());
    await expect(input).toBeVisible();
    await expect(input).toHaveAccessibleDescription(
      "Enter valid JSON before saving. Your edits have been kept.",
    );
  },
};
export const Saving: Story = {
  parameters: scenario(
    session,
    http.put(draftPath, async () => {
      await delay("infinite");
      return HttpResponse.json(revision);
    }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await edit(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Save revision" }),
    );
    await expect(
      await canvas.findByRole("button", { name: "Saving…" }),
    ).toBeDisabled();
  },
};
export const ReloadConfirmation: Story = {
  play: async ({ canvasElement }) => {
    const canvas = await edit(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Reload current" }),
    );
    const dialog = await within(canvasElement.ownerDocument.body).findByRole(
      "alertdialog",
      { name: "Discard unsaved configuration?" },
    );
    await waitFor(() => expect(dialog).toBeVisible());
  },
};
export const ReloadFailure: Story = {
  parameters: scenario(
    session,
    http.get(draftPath, () => HttpResponse.json(revision), { once: true }),
    http.get(draftPath, () => HttpResponse.error()),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await configure(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Reload current" }),
    );
    await expect(await canvas.findByRole("alert")).toBeVisible();
    await expect(
      within(canvas.getByRole("tabpanel", { name: "Configuration" })).getByText(
        "Saved revision",
        { exact: true },
      ),
    ).toBeVisible();
  },
};
export const RevisionComparison: Story = {
  parameters: scenario(
    session,
    http.get(`${environmentRoot}/revisions/:revisionId`, () =>
      HttpResponse.json({
        ...revision,
        hash: "bc91d8f76ee0",
        specification: {
          ...revision.specification,
          variables: [{ name: "MODE", value: "previous" }],
        },
      }),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await configure(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Compare revision" }),
    );
    const heading = await canvas.findByRole("heading", {
      name: "Revision comparison",
    });
    await waitFor(() => expect(heading).toHaveFocus());
    await expect(
      canvas.queryByRole("heading", { name: "Another revision was saved" }),
    ).not.toBeInTheDocument();
  },
};
export const ReadOnlyConfiguration: Story = {
  parameters: Viewer.parameters,
  play: async ({ canvasElement }) => {
    const canvas = await configure(canvasElement);
    await expect(
      canvas.queryByRole("button", { name: "Save revision" }),
    ).not.toBeInTheDocument();
  },
};
export const ComposeConfiguration: Story = {
  args: Compose.args,
  play: async ({ canvasElement }) => {
    await configure(canvasElement);
  },
};
export const ReadOnlyCompose: Story = {
  args: Compose.args,
  parameters: scenario(
    { ...session, workspaces: [{ ...workspace, role: "viewer" }] },
    http.get(draftPath, () =>
      HttpResponse.json({
        ...composeRevision,
        specification: {
          ...composeRevision.specification,
          compose: {
            ...composeRevision.specification.compose!,
            files: [
              {
                path: "compose.yaml",
                content:
                  Array.from(
                    { length: 100 },
                    (_, index) => `# Source line ${index + 1}`,
                  ).join("\n") +
                  "\nservices:\n  api:\n    image: nginx:alpine\n",
              },
            ],
          },
        },
      }),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await configure(canvasElement);
    await expect(
      canvas.getByRole("textbox", { name: "Source compose.yaml" }),
    ).toHaveAttribute("aria-readonly", "true");
  },
};

export const ComposeDiagnostics: Story = {
  args: Compose.args,
  parameters: scenario(
    session,
    http.post(`${environmentRoot}/compose/preview`, () =>
      HttpResponse.json({
        diagnostics: [
          {
            path: "compose.yaml",
            line: 3,
            column: 5,
            message: "Choose a pinned image for this service.",
          },
        ],
        permissions: [],
      }),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await configure(canvasElement);
    await expect(
      await canvas.findByText(
        "Source needs attention; graph shows the last valid source.",
      ),
    ).toBeVisible();
    await expect(
      canvas.getByRole("button", { name: "compose.yaml:3:5" }),
    ).toBeVisible();
  },
};
export const ComposeFileReview: Story = {
  args: Compose.args,
  play: async ({ canvasElement }) => {
    const canvas = await configure(canvasElement);
    await userEvent.click(
      canvas.getByText("Bundle files and entrypoints", { exact: true }),
    );
    await userEvent.type(
      canvas.getByLabelText("New relative file path", {}),
      "notes.txt",
    );
    await userEvent.click(canvas.getByRole("button", { name: "Add file" }));
    await userEvent.click(
      canvas.getByText("Staged file diff (1)", { exact: true }),
    );
    await expect(
      canvas.getByRole("button", { name: "Save revision" }),
    ).toBeEnabled();
  },
};

export const Reloading: Story = {
  parameters: scenario(
    session,
    http.get(draftPath, () => HttpResponse.json(revision), { once: true }),
    http.get(draftPath, async () => {
      await delay("infinite");
      return HttpResponse.json(revision);
    }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await edit(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Reload current" }),
    );
    const body = within(canvasElement.ownerDocument.body);
    const dialog = await body.findByRole("alertdialog", {
      name: "Discard unsaved configuration?",
    });
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Discard and reload" }),
    );
    await expect(
      await canvas.findByRole("button", { name: "Reloading…" }),
    ).toBeDisabled();
    await waitFor(() =>
      expect(
        canvas.getByRole("heading", { name: "Configuration", level: 2 }),
      ).toHaveFocus(),
    );
  },
};
