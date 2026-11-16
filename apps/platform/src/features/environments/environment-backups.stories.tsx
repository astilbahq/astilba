import type { Meta, StoryObj } from "@storybook/tanstack-react";
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
    await expect(await dialog.findByRole("alert")).toBeVisible();
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
