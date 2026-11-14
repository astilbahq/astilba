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
import {
  confirmPurge,
  keptVolumes,
  listed,
  listing,
  members,
  noVolumes,
  openPurgeDialog,
  operation,
  ownerId,
  pollWait,
  retired,
  volumes,
} from "../../workshop/retirement-fixtures";

const path = `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${environments[0].id}`;
const meta = {
  title: "Pages/Environment purge",
  component: ShellPreview,
  args: { initialPath: path },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

const purgeOperationId = "00000000-0000-4000-8000-000000000061";
const purging: Schema["Environment"] = {
  ...retired,
  state: "purging",
  purgedAt: "2026-11-12T10:00:00Z",
  purgedBy: ownerId,
  purgeOperationId,
};
const purged: Schema["Environment"] = { ...purging, state: "purged" };
// A revoked server is never asked, so its purge has no operation.
const purgedRevoked: Schema["Environment"] = {
  ...purged,
  purgeOperationId: null,
  purgedWithoutHost: true,
};
const withoutHostTitle =
  "This environment’s secret values are erased, but its volumes were not deleted";
const deletedVolumes = http.get(`${environmentRoot}/volumes`, () =>
  HttpResponse.json(
    keptVolumes.map((volume) => ({ ...volume, deleted: true })),
  ),
);
const dialogName = "Delete the retained data of Development?";

// The dialog names every kept volume, and the request lists exactly those volumes.
let purgeRequests: Schema["EnvironmentPurgeRequest"][] = [];
let purgeAccepted = false;
export const PurgeDialog: Story = {
  beforeEach: () => {
    purgeRequests = [];
    purgeAccepted = false;
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(listed(purgeAccepted ? purging : retired)),
    ),
    http.post(`${environmentRoot}/purge`, async ({ request }) => {
      purgeRequests.push(
        (await request.json()) as Schema["EnvironmentPurgeRequest"],
      );
      purgeAccepted = true;
      return HttpResponse.json(purging, { status: 201 });
    }),
    operation("queued", "", purgeOperationId),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const banner = await canvas.findByRole("region", {
      name: "This environment is retired",
    });
    await expect(
      within(banner).getByRole("button", { name: "Restore environment" }),
    ).toBeVisible();
    const regions = canvas.getAllByRole("status");
    const dialog = await openPurgeDialog(canvasElement);
    await expect(dialog).toHaveAccessibleDescription(
      /cannot be undone, and the environment cannot be restored afterwards\..*Its server is asked to delete these data volumes: postgres-data uploads.*secrets are erased.*Backups at the workspace’s backup destination are kept\./,
    );
    const submit = within(dialog).getByRole("button", {
      name: "Delete retained data",
    });
    await expect(submit).toBeDisabled();
    await confirmPurge(dialog);
    await waitFor(() => expect(purgeRequests).toHaveLength(1));
    await expect(purgeRequests[0]).toEqual({
      confirmName: "Development",
      volumeIds: keptVolumes.map((volume) => volume.id),
    });
    const message = await canvas.findByText(
      "Deleting retained data. Its secret values are erased, and its server is deleting its volumes.",
    );
    await expect(regions).toContain(message.closest('[role="status"]'));
    await canvas.findByRole("heading", {
      name: "This environment’s retained data is being deleted",
    });
    await expect(
      canvas.queryByRole("button", { name: "Restore environment" }),
    ).toBeNull();
  },
};

// A session that is not recent confirms identity in place before the request.
export const PurgeIdentity: Story = {
  parameters: scenario(
    { ...session, recentAuth: false },
    listing(retired),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const dialog = await openPurgeDialog(canvasElement);
    await confirmPurge(dialog);
    const confirmation = await within(
      canvasElement.ownerDocument.body,
    ).findByRole("alertdialog", { name: "Confirm your identity" });
    await userEvent.click(
      within(confirmation).getByRole("button", { name: "Cancel" }),
    );
    await waitFor(() => expect(confirmation).not.toBeInTheDocument());
    await expect(within(dialog).getByLabelText("Environment name")).toHaveValue(
      "Development",
    );
  },
};

// The kept volumes changed after the dialog opened. The refusal reloads the list, and the next
// request names only the volumes now shown.
let volumesChanged = false;
let changedRequests: Schema["EnvironmentPurgeRequest"][] = [];
export const PurgeVolumesChanged: Story = {
  beforeEach: () => {
    volumesChanged = false;
    changedRequests = [];
  },
  parameters: scenario(
    session,
    listing(retired),
    http.get(`${environmentRoot}/volumes`, () =>
      HttpResponse.json(volumesChanged ? keptVolumes.slice(0, 1) : keptVolumes),
    ),
    http.post(`${environmentRoot}/purge`, async ({ request }) => {
      changedRequests.push(
        (await request.json()) as Schema["EnvironmentPurgeRequest"],
      );
      if (changedRequests.length > 1)
        return HttpResponse.json(purging, { status: 201 });
      volumesChanged = true;
      return HttpResponse.json(
        {
          code: "conflict",
          message: "Server text the dashboard does not show.",
          requestId: "workshop",
          fields: ["retirement.volumes"],
        } satisfies Schema["Error"],
        { status: 409 },
      );
    }),
    operation("queued", "", purgeOperationId),
    members,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const element = await openPurgeDialog(canvasElement);
    const dialog = within(element);
    await confirmPurge(element);
    await expect(await dialog.findByRole("alert")).toHaveTextContent(
      "The volumes this environment keeps have changed. Check the updated list, then confirm again.",
    );
    await waitFor(() =>
      expect(element).not.toHaveAccessibleDescription(/uploads/),
    );
    await expect(element).toHaveAccessibleDescription(/postgres-data/);
    await expect(dialog.getByLabelText("Environment name")).toHaveValue(
      "Development",
    );
    await userEvent.click(
      dialog.getByRole("button", { name: "Delete retained data" }),
    );
    await waitFor(() => expect(changedRequests).toHaveLength(2));
    await expect(changedRequests[1].volumeIds).toEqual([keptVolumes[0].id]);
  },
};

// An environment that kept no volumes is purged at once, which the page announces.
let purgedNow = false;
export const PurgeWithoutVolumes: Story = {
  beforeEach: () => {
    purgedNow = false;
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(
        listed(purgedNow ? { ...purged, purgeOperationId: null } : retired),
      ),
    ),
    http.post(`${environmentRoot}/purge`, async ({ request }) => {
      const body = (await request.json()) as Schema["EnvironmentPurgeRequest"];
      if (body.volumeIds.length) return HttpResponse.json({}, { status: 500 });
      purgedNow = true;
      return HttpResponse.json(
        { ...purged, purgeOperationId: null },
        { status: 201 },
      );
    }),
    members,
    noVolumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const dialog = await openPurgeDialog(canvasElement);
    await expect(dialog).toHaveAccessibleDescription(
      /It keeps no data volumes, so nothing is deleted from its server\./,
    );
    await confirmPurge(dialog);
    await canvas.findByText("Retained data deleted.");
    // The dialog's trigger is gone, so focus moves to the banner's heading.
    const heading = await canvas.findByRole("heading", {
      name: "This environment’s retained data is deleted",
    });
    await waitFor(() => expect(heading).toHaveFocus());
    // Nothing was on a server to delete, so the banner does not claim it was.
    await expect(
      canvas.getByText(
        "It is read-only and cannot be restored. It kept no data volumes, and its secret values were erased.",
      ),
    ).toBeVisible();
  },
};

// A failed deletion can be asked for again, with the same confirmation.
export const PurgingFailed: Story = {
  parameters: scenario(
    session,
    listing(purging),
    members,
    volumes,
    operation(
      "failed",
      "The server's agent stopped before deleting volumes.",
      purgeOperationId,
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const banner = within(
      await canvas.findByRole("region", {
        name: "This environment’s retained data is being deleted",
      }),
    );
    await banner.findByText(
      "Deletion did not finish. Delete retained data again to queue it again.",
    );
    await expect(
      banner.queryByRole("button", { name: "Restore environment" }),
    ).toBeNull();
    await userEvent.click(
      banner.getByRole("button", { name: "Delete retained data" }),
    );
    await expect(
      await within(canvasElement.ownerDocument.body).findByRole("alertdialog", {
        name: dialogName,
      }),
    ).toHaveAccessibleDescription(/postgres-data uploads/);
  },
};
export const PurgingWaiting: Story = {
  parameters: scenario(
    session,
    listing(purging),
    members,
    volumes,
    operation("waiting_for_agent", "", purgeOperationId),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText(
      "Deletion from the server: waiting for the server’s agent",
    );
    await expect(canvas.queryByRole("button", { name: /Restore/ })).toBeNull();
    await expect(
      canvas.queryByRole("button", { name: "Delete retained data" }),
    ).toBeNull();
  },
};
// The server's confirmation is announced from a region that was already there.
let deleted = false;
export const PurgeFinishes: Story = {
  beforeEach: () => {
    deleted = false;
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(listed(deleted ? purged : purging)),
    ),
    members,
    volumes,
    operation("running", "", purgeOperationId),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("heading", {
      name: "This environment’s retained data is being deleted",
    });
    const regions = canvas.getAllByRole("status");
    deleted = true;
    const message = await canvas.findByText(
      "Retained data deleted.",
      {},
      { timeout: pollWait },
    );
    await expect(regions).toContain(message.closest('[role="status"]'));
  },
};
// Opening a purged environment announces nothing and offers no lifecycle action.
export const Purged: Story = {
  parameters: scenario(
    session,
    listing(purged),
    members,
    deletedVolumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const element = await canvas.findByRole("region", {
      name: "This environment’s retained data is deleted",
    });
    const banner = within(element);
    await waitFor(() =>
      expect(element).toHaveTextContent(
        /Ada Lovelace asked to delete its retained data on/,
      ),
    );
    await expect(
      banner.getByText(
        "It is read-only and cannot be restored. Its data volumes were deleted from its server, and its secret values were erased.",
      ),
    ).toBeVisible();
    await expect(banner.queryByRole("heading", { level: 3 })).toBeNull();
    await expect(canvas.queryByRole("button", { name: /Restore/ })).toBeNull();
    await expect(
      canvas.queryByRole("button", { name: "Delete retained data" }),
    ).toBeNull();
    await expect(canvas.queryByText("Retained data deleted.")).toBeNull();
    await userEvent.click(canvas.getByRole("tab", { name: "Settings" }));
    await expect(
      await canvas.findByRole("heading", { name: "Secrets" }),
    ).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: "Retire environment" }),
    ).toBeNull();
  },
};
// A revoked server was never asked to delete the volumes, which may still be on it.
export const PurgedWithoutHost: Story = {
  parameters: scenario(
    session,
    listing(purgedRevoked),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const banner = within(
      await within(canvasElement).findByRole("region", {
        name: withoutHostTitle,
      }),
    );
    await expect(
      banner.getByText(/must be removed there by hand\./),
    ).toBeVisible();
    await expect(
      banner.getByRole("heading", {
        name: "Volumes that may remain on the server",
      }),
    ).toBeVisible();
    await expect(await banner.findByText("postgres-data")).toBeVisible();
    await expect(banner.queryByText(/were deleted from its server/)).toBeNull();
  },
};
// The purge's own result says its server was revoked, which the announcement follows.
let revokedNow = false;
export const PurgeRevokedServer: Story = {
  beforeEach: () => {
    revokedNow = false;
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(listed(revokedNow ? purgedRevoked : retired)),
    ),
    http.post(`${environmentRoot}/purge`, () => {
      revokedNow = true;
      return HttpResponse.json(purgedRevoked, { status: 201 });
    }),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const dialog = await openPurgeDialog(canvasElement);
    await expect(dialog).toHaveAccessibleDescription(
      /Its server is asked to delete these data volumes: postgres-data uploads If its server has been revoked, it is not contacted, and these volumes stay on it\./,
    );
    await confirmPurge(dialog);
    await canvas.findByText(
      "Secret values erased. Its server was revoked, so its volumes were not deleted.",
    );
    await expect(canvas.queryByText("Retained data deleted.")).toBeNull();
    await canvas.findByRole("heading", { name: withoutHostTitle });
  },
};
// A failed read of the kept volumes can be asked for again, in the banner and in the dialog.
// Reads fail until the play function lets them through, since opening the dialog reads again too.
let volumesRecovered = false;
export const VolumesLoadFailed: Story = {
  beforeEach: () => {
    volumesRecovered = false;
  },
  parameters: scenario(
    session,
    listing(retired),
    http.get(`${environmentRoot}/volumes`, () =>
      volumesRecovered
        ? HttpResponse.json(keptVolumes)
        : workshopError("internal", 500),
    ),
    members,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const banner = within(
      await canvas.findByRole("region", {
        name: "This environment is retired",
      }),
    );
    // A retry that fails again leaves focus on the volumes, where the failure is read again. The
    // button itself goes while the read is pending, so it cannot hold focus.
    await userEvent.click(
      await banner.findByRole("button", { name: "Try again" }),
    );
    const region = banner.getByRole("region", { name: "Kept volumes" });
    await waitFor(() => expect(region).toHaveFocus());
    await banner.findByText("Kept volumes could not be loaded.");
    const dialog = await openPurgeDialog(canvasElement);
    const controls = within(dialog);
    // Opening the dialog reads again, which fails too.
    await waitFor(() =>
      expect(dialog).toHaveAccessibleDescription(
        /Kept volumes could not be loaded\./,
      ),
    );
    await userEvent.type(
      controls.getByLabelText("Environment name"),
      "Development",
    );
    const submit = controls.getByRole("button", {
      name: "Delete retained data",
    });
    await expect(submit).toBeDisabled();
    volumesRecovered = true;
    await userEvent.click(
      await controls.findByRole("button", { name: "Try again" }),
    );
    // Focus stays in the dialog, on the item that now lists the volumes.
    await waitFor(() =>
      expect(dialog).toHaveAccessibleDescription(/postgres-data uploads/),
    );
    // The popup itself holds this text too, and is where focus falls without the fix.
    const focused = dialog.ownerDocument.activeElement;
    await expect(dialog).toContainElement(focused as HTMLElement);
    await expect(focused).not.toBe(dialog);
    await expect(focused?.tagName).toBe("LI");
    await expect(focused).toHaveTextContent(
      "Its server is asked to delete these data volumes:",
    );
    await expect(submit).toBeEnabled();
  },
};
