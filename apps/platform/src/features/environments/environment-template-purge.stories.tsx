import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../../api/client";
import {
  ShellPreview,
  session,
  workspace,
  examples,
  projectHandlers,
} from "../../workshop/app-preview";
import { environmentRoot } from "../../workshop/environment-fixtures";
import { scenario } from "../../workshop/fixtures";
import {
  confirmPurge,
  members,
  noVolumes,
  openPurgeDialog,
  template,
  templateListing,
} from "../../workshop/retirement-fixtures";

const templatePath = `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${template.id}`;
const meta = {
  title: "Pages/Environment template purge",
  component: ShellPreview,
  args: { initialPath: templatePath },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

// A preview template's allocated previews hold copies of its secrets, so its purge waits until each
// one is deleted. A request never allocated an environment holds none.
const preview = (
  number: number,
  state: string,
  note: string,
  allocated = true,
): Schema["PullRequestPreview"] => ({
  id: `00000000-0000-4000-8000-0000000001${number}`,
  number,
  head: "0123456789abcdef0123456789abcdef01234567",
  headRepository: 7,
  fork: false,
  state,
  note,
  generation: 1,
  ...(allocated && {
    environmentId: `00000000-0000-4000-8000-0000000002${number}`,
  }),
});
const deletedPreview = preview(
  41,
  "deleted",
  "Preview resources and owned data were removed.",
);
const blockedPreview = preview(
  42,
  "blocked",
  "Cleanup waits for a verified agent transfer.",
);
const queuedRequest = preview(
  43,
  "blocked",
  "Preview policy needs owner approval.",
  false,
);

export const TemplatePurgeWaitsForPreviews: Story = {
  parameters: scenario(
    session,
    templateListing,
    http.get(`${environmentRoot}/previews`, () =>
      HttpResponse.json([blockedPreview, deletedPreview, queuedRequest]),
    ),
    members,
    noVolumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const dialog = await openPurgeDialog(canvasElement, "Pull requests");
    await waitFor(() =>
      expect(dialog).toHaveAccessibleDescription(
        /Its previews hold copies of its secrets, so each must be deleted first\..*Request #42: blocked\. Cleanup waits for a verified agent transfer\./,
      ),
    );
    await expect(dialog).not.toHaveAccessibleDescription(/#41|#43/);
    await userEvent.type(
      within(dialog).getByLabelText("Environment name"),
      "Pull requests",
    );
    await expect(
      within(dialog).getByRole("button", { name: "Delete retained data" }),
    ).toBeDisabled();
  },
};

// A preview that came back after the dialog loaded the list refuses the purge, and the dialog then
// shows it.
let previewCameBack = false;
export const TemplatePurgeRefusedForAPreview: Story = {
  beforeEach: () => {
    previewCameBack = false;
  },
  parameters: scenario(
    session,
    templateListing,
    http.get(`${environmentRoot}/previews`, () =>
      HttpResponse.json(
        previewCameBack
          ? [blockedPreview, deletedPreview, queuedRequest]
          : [deletedPreview, queuedRequest],
      ),
    ),
    http.post(`${environmentRoot}/purge`, () => {
      previewCameBack = true;
      return HttpResponse.json(
        {
          code: "conflict",
          message: "Server text the dashboard does not show.",
          requestId: "workshop",
          fields: ["retirement.previews"],
        } satisfies Schema["Error"],
        { status: 409 },
      );
    }),
    members,
    noVolumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const element = await openPurgeDialog(canvasElement, "Pull requests");
    const dialog = within(element);
    await waitFor(() =>
      expect(element).toHaveAccessibleDescription(
        /None of its previews holds copies of its secrets\./,
      ),
    );
    await confirmPurge(element, "Pull requests");
    await expect(await dialog.findByRole("alert")).toHaveTextContent(
      "Some of this template’s previews are not deleted yet. Each is listed above with what it is waiting for.",
    );
    await waitFor(() =>
      expect(element).toHaveAccessibleDescription(/Request #42: blocked\./),
    );
    await expect(
      dialog.getByRole("button", { name: "Delete retained data" }),
    ).toBeDisabled();
  },
};
