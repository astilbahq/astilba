import type { StoryObj } from "@storybook/tanstack-react";
import type { i18n } from "i18next";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import {
  ShellPreview,
  session,
  environments,
  environmentsPath,
  projectHandlers,
} from "../../workshop/app-preview";
import {
  draftPath,
  environmentRoot,
  revision,
} from "../../workshop/environment-fixtures";
import { scenario } from "../../workshop/fixtures";

type Story = StoryObj<typeof ShellPreview>;

export const editedDraft = JSON.stringify(
  {
    version: 1,
    applications: [],
    variables: [{ name: "MODE", value: "development" }],
  },
  null,
  2,
);
let releaseEnvironmentList: () => void;
let environmentListGate: Promise<void>;
let environmentListAttempts = 0;
export const environmentListRecovery: Story = {
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
let releaseRollback: () => void;
let rollbackGate: Promise<void>;
let rollbackStarted = false;
export const rollbackAfterEditing: Story = {
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
    const deployments = await canvas.findByRole("tab", { name: "Deployments" });
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
export async function edit(canvasElement: HTMLElement) {
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
export async function configure(canvasElement: HTMLElement, instance?: i18n) {
  const canvas = within(canvasElement);
  await userEvent.click(
    await canvas.findByRole("button", {
      name: instance
        ? (name) =>
            name ===
            instance.t(($) => $.openConfiguration, { ns: "environment" })
        : "Open configuration",
    }),
  );
  return canvas;
}
export const longNames: Story = {
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
