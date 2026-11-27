import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import {
  ShellPreview,
  session,
  workspace,
  examples,
  environments,
  projectHandlers,
} from "../../workshop/app-preview";
import {
  resetRevokedServerBackups,
  revokedServerBackupHandlers,
} from "../../workshop/backup-recovery-fixtures";
import { account } from "../../workshop/backup-storage-fixtures";
import { scenario } from "../../workshop/fixtures";

const meta = {
  title: "Pages/Environment backups",
  component: ShellPreview,
  args: {
    initialPath: `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${environments[0].id}`,
  },
  beforeEach: resetRevokedServerBackups,
  parameters: scenario(
    session,
    ...revokedServerBackupHandlers(),
    ...projectHandlers(),
  ),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

async function discardFirstRestore(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.click(
    await canvas.findByRole("tab", { name: "Deployments" }),
  );
  const [discard] = await canvas.findAllByRole("button", {
    name: "Discard restored volume",
  });
  await userEvent.click(discard);
  const dialog = await within(canvasElement.ownerDocument.body).findByRole(
    "alertdialog",
  );
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Discard volume" }),
  );
  return { canvas, dialog: within(dialog) };
}

// A policy whose server was revoked offers its recorded snapshots for a restore onto the
// replacement, and its destination is marked as exposed.
export const RevokedServerBackups: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("tab", { name: "Deployments" }),
    );
    await expect(
      await canvas.findByRole("region", {
        name: "Restore from a revoked server",
      }),
    ).toBeVisible();
    // Every receipt is offered, and none is chosen for the owner.
    await expect(
      await canvas.findAllByRole("radio", {
        name: /Recorded by Astilba/,
        checked: false,
      }),
    ).toHaveLength(2);
    // A confirmed discard hides the restore's actions; its running status takes their focus.
    await discardFirstRestore(canvasElement);
    await waitFor(() =>
      expect(canvas.getByText(/^Discarding the volume\./)).toHaveFocus(),
    );
    await expect(
      canvas.queryByRole("button", { name: "Prepare cutover draft" }),
    ).not.toBeInTheDocument();
  },
};

// Another owner's discard started first: the refusal stays in the open dialog, and closing it
// hands focus to the restore's status, as its actions have gone.
export const RefusedDiscard: Story = {
  parameters: scenario(
    session,
    ...revokedServerBackupHandlers({ refuseDiscard: true }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const { canvas, dialog } = await discardFirstRestore(canvasElement);
    // The refusal the server names reads from the catalogue, and the dialog offers no second attempt.
    await expect(await dialog.findByRole("alert")).toHaveTextContent(
      "This volume is already being discarded.",
    );
    await waitFor(() =>
      expect(
        dialog.getByRole("button", { name: "Discard volume" }),
      ).toHaveAttribute("aria-disabled", "true"),
    );
    await expect(
      canvas.getByText(/^Discarding the volume\./),
    ).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(canvas.getByText(/^Discarding the volume\./)).toHaveFocus(),
    );
    await expect(
      canvas.queryByRole("button", { name: "Prepare cutover draft" }),
    ).not.toBeInTheDocument();
  },
};

// The reload after an accepted discard fails: the restore still shows the discard and holds its
// volume, and the next load, by when the discard has failed, replaces it with the server's record.
export const DiscardOutlivesAFailedReload: Story = {
  parameters: scenario(
    session,
    ...revokedServerBackupHandlers({ failReload: true }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const { canvas } = await discardFirstRestore(canvasElement);
    await waitFor(() =>
      expect(canvas.getByText(/^Discarding the volume\./)).toHaveFocus(),
    );
    await expect(
      canvas.queryByRole("button", { name: "Prepare cutover draft" }),
    ).not.toBeInTheDocument();
    await expect(
      await canvas.findByText(
        /wasn't discarded: The server couldn't delete the volume\./,
        undefined,
        { timeout: 8000 },
      ),
    ).toBeVisible();
    await expect(
      canvas.getByRole("button", { name: "Prepare cutover draft" }),
    ).toBeVisible();
  },
};

// The discard starts but its reply is lost: the dialog keeps the error, and says why it offers no
// second attempt.
export const LostDiscardReply: Story = {
  parameters: scenario(
    session,
    ...revokedServerBackupHandlers({ loseReply: true }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const { dialog } = await discardFirstRestore(canvasElement);
    await expect(await dialog.findByRole("alert")).toHaveTextContent(
      "The platform couldn't be reached.",
    );
    await waitFor(() =>
      expect(dialog.getByRole("status")).toHaveTextContent(
        /^Discarding the volume\./,
      ),
    );
    await expect(
      dialog.getByRole("button", { name: "Discard volume" }),
    ).toHaveAttribute("aria-disabled", "true");
  },
};

// A destination's keys belong to the server it was created for, so a new policy elsewhere can't
// choose the revoked server's.
export const AnotherServersDestination: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("tab", { name: "Deployments" }),
    );
    await userEvent.click(
      await canvas.findByText("Create backup policy or container job"),
    );
    const form = await canvas.findByRole("form", {
      name: "Create backup policy",
    });
    const option = await within(form).findByRole("option", {
      name: /^Offsite bucket — keys belong to edge-1/,
    });
    await expect(option).toBeDisabled();
  },
};

// With a storage account, a destination can have management make its server's key: the form then
// asks for the account and an AWS region, and for no endpoint or keys.
export const ManagedDestinationForm: Story = {
  parameters: scenario(
    session,
    http.get("/api/v1/workspaces/:workspaceId/backup-storage-accounts", () =>
      HttpResponse.json([account]),
    ),
    ...revokedServerBackupHandlers(),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("tab", { name: "Deployments" }),
    );
    await userEvent.click(
      await canvas.findByText("Add a reusable backup destination"),
    );
    const form = within(
      await canvas.findByRole("form", { name: "Add backup destination" }),
    );
    await userEvent.selectOptions(
      form.getByLabelText("Storage type"),
      await form.findByRole("option", {
        name: "AWS S3, with keys management makes",
      }),
    );
    await expect(
      form.getByLabelText("Storage account the key is made from"),
    ).toHaveValue(account.id);
    await expect(
      form.getByLabelText(/^AWS region, such as eu-west-2/),
    ).toBeVisible();
    await expect(form.queryByLabelText("S3 HTTPS origin")).toBeNull();
    await expect(form.queryByLabelText("S3 access key")).toBeNull();
    await expect(form.queryByLabelText("S3 secret key")).toBeNull();
  },
};
