import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../api/client";
import { hostedMember, scenario } from "../workshop/fixtures";
import { WorkspaceSettings } from "./workspace-settings";

const workspace = hostedMember.workspaces[0];
const root = "/api/v1/workspaces/:workspaceId";
const approver = {
  approverId: "00000000-0000-4000-8000-000000000020",
  approverName: "Sam",
  approverEmail: "sam@example.test",
};
const place = {
  projectId: "00000000-0000-4000-8000-000000000030",
  projectName: "Website",
  environmentId: "00000000-0000-4000-8000-000000000031",
  environmentName: "Production",
};
const nowhere = {
  projectId: null,
  projectName: null,
  environmentId: null,
  environmentName: null,
};
const approvals: Schema["MfaRequirementApproval"][] = [
  { ...approver, ...place, kind: "gitSource", subject: "" },
  { ...approver, ...place, kind: "buildTarget", subject: "api" },
  { ...approver, ...nowhere, kind: "invitation", subject: "kim@example.test" },
];

// saved records each workspace settings body the page sends.
function settings(
  listed: Schema["MfaRequirementApproval"][],
  saved: unknown[],
) {
  return scenario(
    hostedMember,
    http.get(`${root}/mfa-requirement`, () =>
      HttpResponse.json({ approvals: listed }),
    ),
    http.put(root, async ({ request }) => {
      const body = (await request.json()) as Schema["WorkspaceSettings"];
      saved.push(body);
      return HttpResponse.json({ ...workspace, ...body });
    }),
  );
}

const meta = {
  title: "Pages/Workspace settings",
  component: WorkspaceSettings,
  args: { workspace, onSaved: async () => {} },
  render: (args) => (
    <main>
      <h1>{workspace.name}</h1>
      <WorkspaceSettings {...args} />
    </main>
  ),
} satisfies Meta<typeof WorkspaceSettings>;
export default meta;
type Story = StoryObj<typeof meta>;

const confirmedSaves: unknown[] = [];
export const RequiringTwoFactorListsWhatStops: Story = {
  parameters: settings(approvals, confirmedSaves),
  play: async ({ canvasElement }) => {
    confirmedSaves.length = 0;
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      canvas.getByRole("checkbox", {
        name: "Require two-factor authentication for changes",
      }),
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Save settings" }),
    );
    const dialog = within(await body.findByRole("alertdialog"));
    await expect(dialog.getByText("Sam")).toBeVisible();
    await expect(
      dialog.getByText(
        "Automatic imports and deployments from Git in Website / Production",
      ),
    ).toBeVisible();
    await expect(
      dialog.getByText("Automatic builds of api in Website / Production"),
    ).toBeVisible();
    await expect(
      dialog.getByText("Invitation for kim@example.test"),
    ).toBeVisible();
    // Nothing is saved until the owner confirms.
    await expect(confirmedSaves).toHaveLength(0);
    await userEvent.click(
      dialog.getByRole("button", { name: "Require it and stop these" }),
    );
    await waitFor(() =>
      expect(confirmedSaves).toEqual([
        { name: workspace.name, requireMfa: true },
      ]),
    );
  },
};

const cancelledSaves: unknown[] = [];
export const CancellingSavesNothing: Story = {
  parameters: settings(approvals, cancelledSaves),
  play: async ({ canvasElement }) => {
    cancelledSaves.length = 0;
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      canvas.getByRole("checkbox", {
        name: "Require two-factor authentication for changes",
      }),
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Save settings" }),
    );
    const dialog = within(await body.findByRole("alertdialog"));
    await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull());
    await expect(cancelledSaves).toHaveLength(0);
  },
};

const directSaves: unknown[] = [];
export const NothingToStopSavesAtOnce: Story = {
  parameters: settings([], directSaves),
  play: async ({ canvasElement }) => {
    directSaves.length = 0;
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("checkbox", {
        name: "Require two-factor authentication for changes",
      }),
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Save settings" }),
    );
    await waitFor(() =>
      expect(directSaves).toEqual([{ name: workspace.name, requireMfa: true }]),
    );
    await expect(
      within(canvasElement.ownerDocument.body).queryByRole("alertdialog"),
    ).toBeNull();
  },
};
