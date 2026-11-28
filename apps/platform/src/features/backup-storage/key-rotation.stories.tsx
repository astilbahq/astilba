import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { ShellPreview, session, workspace } from "../../workshop/app-preview";
import {
  backupStorageHandlers,
  releaseHeldServers,
  resetBackupStorage,
  savedRotation,
} from "../../workshop/backup-storage-fixtures";
import { scenario } from "../../workshop/fixtures";
import { BackupStorage } from "./backup-storage";

// Rotating a managed destination's key, and how often management does it itself.
const meta = {
  title: "Pages/Backup storage accounts/Key rotation",
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

// Rotating asks for a new key without revoking the server, which the destination then says it is
// waiting for, and focus moves to the destination once the action has gone.
export const RotateKey: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const heading = await canvas.findByRole("heading", {
      name: "Edge backups",
    });
    const section = within(heading.closest("section") as HTMLElement);
    await userEvent.click(
      await section.findByRole("button", { name: "Rotate key" }),
    );
    const dialog = within(
      await within(canvasElement.ownerDocument.body).findByRole("alertdialog"),
    );
    await expect(
      dialog.getByText(/The old key keeps working until the server reports/),
    ).toBeVisible();
    await expect(dialog.getByText(/revoke it instead/)).toBeVisible();
    await userEvent.click(dialog.getByRole("button", { name: "Rotate key" }));
    await waitFor(() => expect(heading).toHaveFocus());
    await expect(
      section.getByText(
        "A new key has been asked for. Management is making it.",
      ),
    ).toBeVisible();
    await expect(
      section.queryByRole("button", { name: "Rotate key" }),
    ).not.toBeInTheDocument();
  },
};

// Until the servers have loaded, whether a destination's server is revoked isn't known, so no key
// is offered for rotation. Once they have, only the live server's is.
export const ServersLoading: Story = {
  parameters: scenario(
    session,
    ...backupStorageHandlers({ holdServers: true }),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const live = within(
      (await canvas.findByRole("heading", { name: "Edge backups" })).closest(
        "section",
      ) as HTMLElement,
    );
    const revoked = within(
      canvas
        .getByRole("heading", { name: "Old edge backups" })
        .closest("section") as HTMLElement,
    );
    // The destinations are shown, naming their servers by ID while the list is held.
    await expect(live.getByText(/Keys for server [0-9a-f]{8}$/)).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: "Rotate key" }),
    ).not.toBeInTheDocument();
    releaseHeldServers();
    await expect(
      await live.findByRole("button", { name: "Rotate key" }),
    ).toBeVisible();
    await expect(revoked.getByText(/Keys for edge-0$/)).toBeVisible();
    await expect(
      revoked.queryByRole("button", { name: "Rotate key" }),
    ).not.toBeInTheDocument();
  },
};

// A server revoked since the page loaded has its key's rotation refused in the dialog. The
// servers are shown again as they are now, so the destination offers no rotation, and focus moves
// to it once the dialog closes.
export const RefusedRotation: Story = {
  parameters: scenario(
    session,
    ...backupStorageHandlers({ revokeOnRotate: true }),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const heading = await canvas.findByRole("heading", {
      name: "Edge backups",
    });
    const section = within(heading.closest("section") as HTMLElement);
    await userEvent.click(
      await section.findByRole("button", { name: "Rotate key" }),
    );
    const body = within(canvasElement.ownerDocument.body);
    const dialog = within(await body.findByRole("alertdialog"));
    await userEvent.click(dialog.getByRole("button", { name: "Rotate key" }));
    await expect(await dialog.findByRole("alert")).toHaveTextContent(
      /server is revoked, so its keys aren.t replaced\./,
    );
    await expect(body.getByRole("alertdialog")).toBeVisible();
    // The modal hides the page from assistive technology while it is open.
    await waitFor(() =>
      expect(
        section.queryByRole("button", { name: "Rotate key", hidden: true }),
      ).not.toBeInTheDocument(),
    );
    await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(heading).toHaveFocus());
  },
};

// Turning key rotation off is a choice of its own, which the account then shows.
export const TurnOffKeyRotation: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const account = within(
      await canvas.findByRole("article", { name: "Production AWS" }),
    );
    await userEvent.click(account.getByText("Change key rotation"));
    const form = within(
      account.getByRole("form", { name: "Key rotation of Production AWS" }),
    );
    await expect(
      form.getByLabelText("Maximum key age in days, from 30 to 365"),
    ).toHaveValue(90);
    await userEvent.click(
      form.getByRole("radio", { name: /^Don’t replace keys automatically/ }),
    );
    await expect(
      form.queryByLabelText("Maximum key age in days, from 30 to 365"),
    ).not.toBeInTheDocument();
    await userEvent.click(
      form.getByRole("button", { name: "Save key rotation" }),
    );
    await expect(await account.findByText("Key rotation saved.")).toBeVisible();
    await expect(savedRotation()).toEqual({ days: null });
    await expect(
      await account.findByText("Off. Keys stay in use until you rotate them."),
    ).toBeVisible();
    // The destinations no longer say when their keys are replaced, and the choice stays made.
    await waitFor(() =>
      expect(
        canvas.queryByText(/^The key was due to be replaced on /),
      ).not.toBeInTheDocument(),
    );
    await expect(
      form.getByRole("radio", { name: /^Don’t replace keys automatically/ }),
    ).toBeChecked();
  },
};

// A period saved from the keyboard keeps focus in its field, which shows the saved period.
export const ShortenKeyRotation: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const account = within(
      await canvas.findByRole("article", { name: "Production AWS" }),
    );
    await userEvent.click(account.getByText("Change key rotation"));
    const days = account.getByLabelText(
      "Maximum key age in days, from 30 to 365",
    );
    await userEvent.clear(days);
    await userEvent.type(days, "60{Enter}");
    await expect(await account.findByText("Key rotation saved.")).toBeVisible();
    await expect(savedRotation()).toEqual({ days: 60 });
    await expect(
      await account.findByText(
        "Management replaces each destination’s key when it is 60 days old.",
      ),
    ).toBeVisible();
    await expect(days).toHaveFocus();
    await expect(days).toHaveValue(60);
  },
};
