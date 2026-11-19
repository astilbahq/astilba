import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../api/client";
import { hostedMember, scenario } from "../workshop/fixtures";
import { workshopFailure } from "../workshop/terminal-session-fixtures";
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
  { ...approver, ...place, kind: "sourceSubmission", subject: "" },
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
      dialog.getByText(
        "Builds of api, and deployments if it deploys in Website / Production",
      ),
    ).toBeVisible();
    await expect(
      dialog.getByText("Invitation for kim@example.test"),
    ).toBeVisible();
    // A deferred action that comes due meanwhile is refused, not paused.
    await expect(
      dialog.getByText(
        "A queued source submission in Website / Production (refused)",
      ),
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
    // Focus returns to the setting the dialog was about.
    await waitFor(() =>
      expect(
        canvas.getByRole("checkbox", {
          name: "Require two-factor authentication for changes",
        }),
      ).toHaveFocus(),
    );
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

const resumedSaves: unknown[] = [];
export const NoLongerRequiringListsWhatResumes: Story = {
  args: { workspace: { ...workspace, requireMfa: true } },
  parameters: settings(approvals, resumedSaves),
  play: async ({ canvasElement }) => {
    resumedSaves.length = 0;
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
    await expect(
      dialog.getByText("Stop requiring two-factor authentication?"),
    ).toBeVisible();
    await expect(
      dialog.getByText("A queued source submission in Website / Production"),
    ).toBeVisible();
    await expect(resumedSaves).toHaveLength(0);
    await userEvent.click(
      dialog.getByRole("button", { name: "Stop requiring it" }),
    );
    await waitFor(() =>
      expect(resumedSaves).toEqual([
        { name: workspace.name, requireMfa: false },
      ]),
    );
  },
};

const refusedSaves: unknown[] = [];
export const ListFailureIsShownAndSavesNothing: Story = {
  parameters: scenario(
    hostedMember,
    http.get(`${root}/mfa-requirement`, () =>
      HttpResponse.json(
        {
          code: "internal",
          message: "The list could not be read.",
          requestId: "workshop",
        },
        { status: 500 },
      ),
    ),
    http.put(root, () => {
      refusedSaves.push("saved");
      return HttpResponse.json(workspace);
    }),
  ),
  play: async ({ canvasElement }) => {
    refusedSaves.length = 0;
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("checkbox", {
        name: "Require two-factor authentication for changes",
      }),
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Save settings" }),
    );
    await expect(await canvas.findByRole("alert")).toBeVisible();
    await expect(refusedSaves).toHaveLength(0);
  },
};

// A member without two-factor authentication confirms with their password.
const passwordMember = {
  ...hostedMember,
  user: { ...hostedMember.user!, mfaEnabled: false },
};
let identityConfirmed = false;
const identitySaves: unknown[] = [];
// The save the dialog confirms asks for identity; once given, focus returns to the setting, since
// the dialog's own button has gone.
export const ConfirmingIdentityReturnsToTheSetting: Story = {
  beforeEach: () => {
    identityConfirmed = false;
    identitySaves.length = 0;
  },
  parameters: scenario(
    passwordMember,
    http.get(`${root}/mfa-requirement`, () => HttpResponse.json({ approvals })),
    http.put(root, async ({ request }) => {
      if (!identityConfirmed)
        return workshopFailure(
          "recent_auth_required",
          "Confirm your identity before this action.",
          403,
        );
      const body = (await request.json()) as Schema["WorkspaceSettings"];
      identitySaves.push(body);
      return HttpResponse.json({ ...workspace, ...body });
    }),
    http.post("/api/v1/auth/reauthenticate", () => {
      identityConfirmed = true;
      return HttpResponse.json(passwordMember);
    }),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    const requirement = canvas.getByRole("checkbox", {
      name: "Require two-factor authentication for changes",
    });
    await userEvent.click(requirement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Save settings" }),
    );
    const dialog = within(await body.findByRole("alertdialog"));
    await userEvent.click(
      dialog.getByRole("button", { name: "Require it and stop these" }),
    );
    const confirmation = within(
      await body.findByRole("alertdialog", { name: "Confirm your identity" }),
    );
    const password = confirmation.getByLabelText("Password");
    await waitFor(() => expect(password).toHaveFocus());
    await userEvent.type(password, "workshop password");
    await userEvent.click(
      confirmation.getByRole("button", { name: "Confirm" }),
    );
    await waitFor(() =>
      expect(identitySaves).toEqual([
        { name: workspace.name, requireMfa: true },
      ]),
    );
    await waitFor(() => expect(requirement).toHaveFocus());
  },
};
