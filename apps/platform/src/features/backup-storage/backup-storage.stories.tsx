import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { ShellPreview, session, workspace } from "../../workshop/app-preview";
import {
  backupStorageHandlers,
  otherInstallation,
  resetBackupStorage,
} from "../../workshop/backup-storage-fixtures";
import { scenario } from "../../workshop/fixtures";
import { BackupStorage } from "./backup-storage";

const meta = {
  title: "Pages/Backup storage accounts",
  component: ShellPreview,
  beforeEach: resetBackupStorage,
  parameters: scenario(session, ...backupStorageHandlers()),
  render: () => (
    <main>
      <h1>{workspace.name}</h1>
      <BackupStorage workspaceId={workspace.id} owner />
    </main>
  ),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

// An account whose check failed and found users management didn't make, and a managed destination
// with an active, a handed-over, a failing and a revoked key.
export const Accounts: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const account = await canvas.findByRole("article", {
      name: "Production AWS",
    });
    const view = within(account);
    await expect(
      view.getByText(/^The latest check failed: AWS refused the request\./),
    ).toBeVisible();
    await expect(view.getByText(/found 1 access key/)).toBeVisible();
    // A peer's user needs nothing, and one without the boundary is left to the AWS console.
    await expect(
      view.getByText("Made by a peer installation. Nothing needs doing."),
    ).toBeVisible();
    await expect(
      view.getByText(/Delete it in the AWS console\./),
    ).toBeVisible();
    await expect(
      view.getAllByRole("button", { name: "Revoke user" }),
    ).toHaveLength(1);
    await expect(
      view.getAllByRole("button", { name: "Add installation as a peer" }),
    ).toHaveLength(1);
    // Keys name their state, a handover and a failing revocation in plain words.
    await expect(
      await view.findByText(/^Revoking it has failed 4 times: AWS refused/),
    ).toBeVisible();
    await expect(
      view.getByText("Handed over to a workspace transfer"),
    ).toBeVisible();
    await expect(
      view.getByText(
        /couldn’t make a key for the renewed server and keeps trying: AWS didn’t answer in time\./,
      ),
    ).toBeVisible();
    await expect(
      view.getAllByRole("button", { name: "Revoke key" }),
    ).toHaveLength(2);
    await expect(view.getByText("1 revoked key")).toBeVisible();
    await userEvent.click(view.getByRole("button", { name: "Retry now" }));
    await expect(
      await view.findByText("Management is trying again."),
    ).toBeVisible();
  },
};

// Revoking a handed-over key warns that it breaks the transferred server's backups, and focus
// moves to the key once its action has gone.
export const RevokeHandedOverKey: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const handedOver = (
      await canvas.findByText("Handed over to a workspace transfer")
    ).closest("li");
    await expect(handedOver).not.toBeNull();
    const item = within(handedOver as HTMLElement);
    await userEvent.click(item.getByRole("button", { name: "Revoke key" }));
    const dialog = within(
      await within(canvasElement.ownerDocument.body).findByRole("alertdialog"),
    );
    await expect(
      dialog.getByText(/breaks the transferred server’s backups/),
    ).toBeVisible();
    await userEvent.click(dialog.getByRole("button", { name: "Revoke key" }));
    await waitFor(() => expect(handedOver).toHaveFocus());
    await expect(item.getByText("Being revoked")).toBeVisible();
    await expect(
      item.queryByRole("button", { name: "Revoke key" }),
    ).not.toBeInTheDocument();
  },
};

// Making another installation a peer asks first, then shows its user as the peer's.
export const AddPeer: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const add = await canvas.findByRole("button", {
      name: "Add installation as a peer",
    });
    const user = add.closest("li");
    await userEvent.click(add);
    const dialog = within(
      await within(canvasElement.ownerDocument.body).findByRole("alertdialog"),
    );
    await expect(
      dialog.getByRole("heading", {
        name: `Add installation ${otherInstallation} as a peer?`,
      }),
    ).toBeVisible();
    await userEvent.click(dialog.getByRole("button", { name: "Add peer" }));
    await waitFor(() => expect(user).toHaveFocus());
    await expect(
      canvas.getAllByText("Made by a peer installation. Nothing needs doing."),
    ).toHaveLength(2);
    await expect(
      canvas.queryByRole("button", { name: "Add installation as a peer" }),
    ).not.toBeInTheDocument();
    await expect(
      canvas.queryByRole("button", { name: "Revoke user" }),
    ).not.toBeInTheDocument();
  },
};

// A refused admin key keeps the owner's draft and names the server's reason.
export const RefusedAccount: Story = {
  parameters: scenario(session, ...backupStorageHandlers({ accounts: [] })),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText(/^No storage accounts yet\./),
    ).toBeVisible();
    await userEvent.click(canvas.getByText("Add a storage account"));
    await userEvent.type(canvas.getByLabelText("Account name"), "Staging AWS");
    await userEvent.type(
      canvas.getByLabelText("Admin access key ID"),
      "AKIAEXAMPLEEXAMPLE",
    );
    await userEvent.type(
      canvas.getByLabelText("Admin secret access key"),
      "example-secret",
    );
    await userEvent.type(
      canvas.getByLabelText(/^Permissions boundary ARN/),
      "arn:aws:iam::123456789012:policy/astilba-backups",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Add storage account" }),
    );
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "AWS did not accept this access key.",
    );
    await expect(canvas.getByLabelText("Account name")).toHaveValue(
      "Staging AWS",
    );
  },
};

// Members other than owners see the accounts and keys without any control.
export const ReadOnly: Story = {
  render: () => (
    <main>
      <h1>{workspace.name}</h1>
      <BackupStorage workspaceId={workspace.id} owner={false} />
    </main>
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByRole("article", { name: "Production AWS" }),
    ).toBeVisible();
    await expect(await canvas.findByText("Edge backups")).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: /^(Revoke|Retry|Check|Add)/ }),
    ).not.toBeInTheDocument();
    await expect(
      canvas.queryByText("Add a storage account"),
    ).not.toBeInTheDocument();
  },
};

export const LoadFailure: Story = {
  parameters: scenario(
    session,
    ...backupStorageHandlers({ failAccounts: true }),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "Storage accounts could not be loaded.",
    );
    await expect(
      canvas.getByRole("button", { name: "Try again" }),
    ).toBeEnabled();
  },
};
