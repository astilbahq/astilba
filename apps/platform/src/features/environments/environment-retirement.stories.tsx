import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../../api/client";
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
  environmentRoot,
  workshopError,
} from "../../workshop/environment-fixtures";
import { scenario } from "../../workshop/fixtures";

const path = `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${environments[0].id}`;
const meta = {
  title: "Pages/Environment retirement",
  component: ShellPreview,
  args: { initialPath: path },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

const ownerId = "00000000-0000-4000-8000-000000000050";
const operationId = "00000000-0000-4000-8000-000000000060";
const retired: Schema["Environment"] = {
  ...environments[0],
  state: "retired",
  retiredAt: "2026-11-10T09:30:00Z",
  retiredBy: ownerId,
  retirementOperationId: operationId,
};
const listing = (environment: Schema["Environment"]) =>
  http.get(environmentsPath, () =>
    HttpResponse.json([environment, ...environments.slice(1)]),
  );
const members = http.get("/api/v1/workspaces/:workspaceId/members", () =>
  HttpResponse.json([
    {
      id: ownerId,
      name: "Ada Lovelace",
      email: "ada@example.com",
      role: "owner",
      mfaEnabled: true,
    },
  ] satisfies Schema["Member"][]),
);
const volumes = http.get(`${environmentRoot}/volumes`, () =>
  HttpResponse.json([
    {
      id: "00000000-0000-4000-8000-000000000070",
      name: "postgres-data",
      inUse: false,
      deleted: false,
    },
    {
      id: "00000000-0000-4000-8000-000000000071",
      name: "uploads",
      inUse: false,
      deleted: false,
    },
  ] satisfies Schema["ManagedVolumes"]),
);
const operation = (state: Schema["ExecutionState"], note: string) =>
  http.get(`${environmentRoot}/operations/:operationId`, () =>
    HttpResponse.json({
      id: operationId,
      serverId: "00000000-0000-4000-8000-000000000080",
      environmentId: environments[0].id,
      generation: 1,
      state,
      kind: "update",
      cancelRequested: false,
      note,
      steps: [],
      createdAt: "2026-11-10T09:30:00Z",
    } satisfies Schema["Operation"]),
  );

async function openRetireDialog(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.click(await canvas.findByRole("tab", { name: "Settings" }));
  await userEvent.click(
    await canvas.findByRole("button", { name: "Retire environment" }),
  );
  const body = within(canvasElement.ownerDocument.body);
  return body.findByRole("alertdialog", { name: "Retire Development?" });
}

export const RetireDialog: Story = {
  play: async ({ canvasElement }) => {
    const dialog = within(await openRetireDialog(canvasElement));
    const submit = dialog.getByRole("button", { name: "Retire environment" });
    await expect(submit).toBeDisabled();
    await userEvent.type(
      dialog.getByLabelText("Environment name"),
      "Development",
    );
    await expect(submit).toBeEnabled();
  },
};
// A session that is not recent confirms identity in place, over the retire dialog, before the
// request; declining leaves the typed name ready to submit again.
export const RetireIdentity: Story = {
  parameters: scenario({ ...session, recentAuth: false }, ...projectHandlers()),
  play: async ({ canvasElement }) => {
    const dialog = within(await openRetireDialog(canvasElement));
    const name = dialog.getByLabelText("Environment name");
    await userEvent.type(name, "Development");
    await userEvent.click(
      dialog.getByRole("button", { name: "Retire environment" }),
    );
    const confirmation = await within(
      canvasElement.ownerDocument.body,
    ).findByRole("alertdialog", { name: "Confirm your identity" });
    await userEvent.click(
      within(confirmation).getByRole("button", { name: "Cancel" }),
    );
    await waitFor(() => expect(confirmation).not.toBeInTheDocument());
    await expect(name).toHaveValue("Development");
  },
};
export const RetireConflict: Story = {
  parameters: scenario(
    session,
    http.post(`${environmentRoot}/retire`, () =>
      HttpResponse.json(
        {
          code: "conflict",
          message:
            "A deployment is in progress in this environment. Wait for it to finish, then retire it.",
          requestId: "workshop",
          fields: ["retirement.active_operation"],
        } satisfies Schema["Error"],
        { status: 409 },
      ),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const dialog = within(await openRetireDialog(canvasElement));
    const name = dialog.getByLabelText("Environment name");
    await userEvent.type(name, "Development");
    await userEvent.click(
      dialog.getByRole("button", { name: "Retire environment" }),
    );
    await expect(await dialog.findByRole("alert")).toHaveTextContent(
      "A deployment is in progress",
    );
    await expect(name).toHaveValue("Development");
  },
};
export const Retired: Story = {
  parameters: scenario(
    session,
    listing(retired),
    members,
    volumes,
    ...projectHandlers(),
  ),
};
export const RetiringFailed: Story = {
  parameters: scenario(
    session,
    listing({ ...retired, state: "retiring" }),
    members,
    volumes,
    operation("failed", "The server's agent stopped before removing services."),
    ...projectHandlers(),
  ),
};
export const RetiringWaiting: Story = {
  parameters: scenario(
    session,
    listing({ ...retired, state: "retiring" }),
    members,
    volumes,
    operation("waiting_for_agent", ""),
    ...projectHandlers(),
  ),
};
export const RetiredWithoutHost: Story = {
  parameters: scenario(
    session,
    listing({ ...retired, retiredWithoutHost: true }),
    members,
    volumes,
    ...projectHandlers(),
  ),
};
export const RetiredViewer: Story = {
  parameters: scenario(
    { ...session, workspaces: [{ ...workspace, role: "viewer" }] },
    listing(retired),
    http.get("/api/v1/workspaces/:workspaceId/members", () =>
      workshopError("forbidden", 403),
    ),
    volumes,
    ...projectHandlers(),
  ),
};
