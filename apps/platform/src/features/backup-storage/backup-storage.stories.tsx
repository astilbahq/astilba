import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { ShellPreview, session, workspace } from "../../workshop/app-preview";
import {
  account,
  acknowledgement,
  backupStorageHandlers,
  lastReplacement,
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

// An account whose check failed and found users management didn't make, one of which AWS won't let
// management delete, and a managed destination with an active, a handed-over, a failing and a
// revoked key.
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
    // A peer's user raises an alert until it is acknowledged, and one without the boundary is left
    // to the AWS console.
    await expect(
      view.getByText(/^Tagged by a peer installation, and acknowledged on /),
    ).toBeVisible();
    await expect(
      view.getByText(/^Tagged by a peer installation\. Someone holding/),
    ).toBeVisible();
    await expect(
      view.getAllByRole("button", { name: "Acknowledge user" }),
    ).toHaveLength(1);
    await expect(
      view.getByText(/Delete it in the AWS console\./),
    ).toBeVisible();
    await expect(
      view.getByText(
        /^Management is revoking it, and its last attempt failed: AWS won’t delete the user .* Remove those in the AWS console so management can delete it\.$/,
      ),
    ).toBeVisible();
    await expect(view.getByText(/^Last checked /)).toBeVisible();
    await expect(view.queryByText(/^Checking…/)).not.toBeInTheDocument();
    // A stranger's user and an unacknowledged peer's user can be revoked; an acknowledged one
    // can't.
    await expect(
      view.getAllByRole("button", { name: "Revoke user" }),
    ).toHaveLength(2);
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
    await expect(view.getByText("Recently revoked keys")).toBeVisible();
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

// A check asked for and not yet reported says so beside when the latest one reported.
export const CheckRunning: Story = {
  parameters: scenario(
    session,
    ...backupStorageHandlers({ accounts: [{ ...account, checking: true }] }),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const view = within(
      await canvas.findByRole("article", { name: "Production AWS" }),
    );
    await expect(
      view.getByText(/^Checking… Last checked /, { selector: "[role=status]" }),
    ).toBeVisible();
  },
};

// A peer's user acknowledged since the page loaded can't be revoked here: the refusal stays in the
// dialog, beside the button that asked.
export const RefusedPeerRevoke: Story = {
  parameters: scenario(
    session,
    ...backupStorageHandlers({ refusePeerRevoke: true }),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const [revoke] = await canvas.findAllByRole("button", {
      name: "Revoke user",
    });
    await userEvent.click(revoke);
    const body = within(canvasElement.ownerDocument.body);
    const dialog = within(await body.findByRole("alertdialog"));
    const confirm = dialog.getByRole("button", { name: "Revoke user" });
    await userEvent.click(confirm);
    await expect(await dialog.findByRole("alert")).toHaveTextContent(
      /acknowledged as a peer installation.s.*Revoke it from that installation\./,
    );
    await expect(body.getByRole("alertdialog")).toBeVisible();
    await expect(confirm).toHaveFocus();
  },
};

// The replacement key keeps the account's boundary, which the form shows and doesn't send.
export const ReplaceKey: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const view = within(
      await canvas.findByRole("article", { name: "Production AWS" }),
    );
    await userEvent.click(view.getByText("Replace the admin key"));
    const form = within(
      view.getByRole("form", {
        name: "Replace the admin key of Production AWS",
      }),
    );
    await expect(
      view.getByText(/^The account keeps this permissions boundary/),
    ).toBeVisible();
    await expect(
      form.queryByLabelText(/^Permissions boundary/),
    ).not.toBeInTheDocument();
    await userEvent.type(
      form.getByLabelText("Admin access key ID"),
      "AKIAEXAMPLEEXAMPLE",
    );
    await userEvent.type(
      form.getByLabelText("Admin secret access key"),
      "example-secret",
    );
    await userEvent.click(
      form.getByRole("button", { name: "Replace admin key" }),
    );
    await expect(
      await view.findByText("Admin key replaced. Delete the old one at AWS."),
    ).toBeVisible();
    await expect(lastReplacement()).toEqual({
      accessKeyId: "AKIAEXAMPLEEXAMPLE",
      secretAccessKey: "example-secret",
      generation: account.generation,
    });
  },
};

// Destinations that fail to load leave each account's controls in place, with a retry.
export const DestinationsLoadFailure: Story = {
  parameters: scenario(
    session,
    ...backupStorageHandlers({ failDestinations: true }),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const destinations = within(
      await canvas.findByRole("region", {
        name: "Destinations using this account",
      }),
    );
    await expect(await destinations.findByRole("alert")).toHaveTextContent(
      "Destinations could not be loaded.",
    );
    await expect(
      destinations.getByRole("button", { name: "Try again" }),
    ).toBeEnabled();
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
    // The user can still be revoked, so focus stays within it.
    await waitFor(() =>
      expect(user).toContainElement(
        canvasElement.ownerDocument.activeElement as HTMLElement,
      ),
    );
    // Its user is the peer's now, and raises an alert until it is acknowledged.
    await expect(
      canvas.getAllByText(/^Tagged by a peer installation\. Someone holding/),
    ).toHaveLength(2);
    await expect(
      canvas.getAllByRole("button", { name: "Acknowledge user" }),
    ).toHaveLength(2);
    await expect(
      canvas.queryByRole("button", { name: "Add installation as a peer" }),
    ).not.toBeInTheDocument();
    // Neither is acknowledged yet, so either can still be revoked.
    await expect(
      canvas.getAllByRole("button", { name: "Revoke user" }),
    ).toHaveLength(2);
  },
};

// Revoking a peer's user the owner hasn't acknowledged warns that a transferred server may still
// use its key, and to replace the admin key.
export const RevokePeerUser: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const user = (
      await canvas.findByText(
        /^Tagged by a peer installation\. Someone holding/,
      )
    ).closest("li");
    if (!user) throw new Error("the peer's user isn't listed");
    await userEvent.click(
      within(user).getByRole("button", { name: "Revoke user" }),
    );
    const dialog = within(
      await within(canvasElement.ownerDocument.body).findByRole("alertdialog"),
    );
    await expect(
      dialog.getByText(/you haven’t acknowledged it\..*replace the admin key/),
    ).toBeVisible();
    await userEvent.click(dialog.getByRole("button", { name: "Revoke user" }));
    await waitFor(() => expect(user).toHaveFocus());
    await expect(
      within(user).getByText("Management is revoking it."),
    ).toBeVisible();
    await expect(
      within(user).queryByRole("button", { name: "Acknowledge user" }),
    ).not.toBeInTheDocument();
  },
};

// Acknowledging a peer's user asks first, naming its access keys, and sends what the owner saw.
export const AcknowledgePeerUser: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const acknowledge = await canvas.findByRole("button", {
      name: "Acknowledge user",
    });
    const user = acknowledge.closest("li");
    await userEvent.click(acknowledge);
    const dialog = within(
      await within(canvasElement.ownerDocument.body).findByRole("alertdialog"),
    );
    const name = "astilba-3b9e7a21-5c48-4d0f-b6e2-9a1c7f3d8e05";
    await expect(
      dialog.getByRole("heading", {
        name: new RegExp(`^Acknowledge ${name} `),
      }),
    ).toBeVisible();
    await expect(
      dialog.getByText(
        /IAM user ID AIDAEXAMPLE000000002 with access keys AKIAEXAMPLE000000002/,
      ),
    ).toBeVisible();
    await userEvent.click(dialog.getByRole("button", { name: "Acknowledge" }));
    await waitFor(() => expect(user).toHaveFocus());
    await expect(acknowledgement(name)).toEqual({
      userId: "AIDAEXAMPLE000000002",
      accessKeyIds: ["AKIAEXAMPLE000000002"],
    });
    await expect(
      canvas.getAllByText(
        /^Tagged by a peer installation, and acknowledged on /,
      ),
    ).toHaveLength(2);
    await expect(
      canvas.queryByRole("button", { name: "Acknowledge user" }),
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
