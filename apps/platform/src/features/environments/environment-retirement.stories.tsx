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
  listed,
  listing,
  members,
  operation,
  operationFixture,
  ownerId,
  pollWait,
  retired,
  retiring,
  volumes,
} from "../../workshop/retirement-fixtures";

const path = `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${environments[0].id}`;
const meta = {
  title: "Pages/Environment retirement",
  component: ShellPreview,
  args: { initialPath: path },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

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
    const element = await openRetireDialog(canvasElement);
    // The effects are part of what the dialog says, not only what it shows.
    await expect(element).toHaveAccessibleDescription(
      /When you retire it:.*Its named data volumes are kept on the server\./,
    );
    const dialog = within(element);
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
// A refusal reads from the catalogue, never the server's English, and keeps the typed name.
export const RetireConflict: Story = {
  parameters: scenario(
    session,
    http.post(`${environmentRoot}/retire`, () =>
      HttpResponse.json(
        {
          code: "conflict",
          message: "Server text the dashboard does not show.",
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
      "Another operation is running for this environment. Wait for it to finish or cancel it, then try again.",
    );
    await expect(name).toHaveValue("Development");
  },
};
let retiredElsewhere = false;
// Another owner retired the environment while the dialog was open. The refusal brings the page
// behind the dialog up to date and keeps the typed name.
export const RetireConflictRefreshes: Story = {
  beforeEach: () => {
    retiredElsewhere = false;
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(listed(retiredElsewhere ? retired : environments[0])),
    ),
    http.post(`${environmentRoot}/retire`, () => {
      retiredElsewhere = true;
      return HttpResponse.json(
        {
          code: "conflict",
          message: "Server text the dashboard does not show.",
          requestId: "workshop",
          fields: ["retirement.state"],
        } satisfies Schema["Error"],
        { status: 409 },
      );
    }),
    members,
    volumes,
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
      "This environment’s state has changed, so this can no longer be done. The page now shows its current state.",
    );
    // The modal hides the page from assistive technology while it is open.
    await expect(
      await within(canvasElement).findByRole("heading", {
        name: "This environment is retired",
        hidden: true,
      }),
    ).toBeInTheDocument();
    await expect(name).toHaveValue("Development");
  },
};
// The completion is announced from a region that was already there while it was retiring.
let removed = false;
export const RetirementFinishes: Story = {
  beforeEach: () => {
    removed = false;
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(listed(removed ? retired : retiring)),
    ),
    members,
    volumes,
    operation("running", ""),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("heading", {
      name: "This environment is being retired",
    });
    const regions = canvas.getAllByRole("status");
    await expect(canvas.queryByText("Environment retired.")).toBeNull();
    removed = true;
    const message = await canvas.findByText(
      "Environment retired.",
      {},
      { timeout: pollWait },
    );
    await expect(regions).toContain(message.closest('[role="status"]'));
  },
};
// Opening an environment that is already retired announces nothing.
export const Retired: Story = {
  parameters: scenario(
    session,
    listing(retired),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("heading", { name: "This environment is retired" });
    await expect(canvas.queryByText("Environment retired.")).toBeNull();
  },
};
export const RestoreDialog: Story = {
  parameters: scenario(
    session,
    listing(retired),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    await userEvent.click(
      await within(canvasElement).findByRole("button", {
        name: "Restore environment",
      }),
    );
    const dialog = await within(canvasElement.ownerDocument.body).findByRole(
      "alertdialog",
      { name: "Restore Development?" },
    );
    await expect(dialog).toHaveAccessibleDescription(
      /as configuration only:.*Nothing is deployed\./,
    );
  },
};
export const RetiringFailed: Story = {
  parameters: scenario(
    session,
    listing(retiring),
    members,
    volumes,
    operation("failed", "The server's agent stopped before removing services."),
    ...projectHandlers(),
  ),
};
export const RetiringWaiting: Story = {
  parameters: scenario(
    session,
    listing(retiring),
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
// Retiring from an active page announces the outcome from a region that was already there, here
// for an environment that was never deployed and so is retired at once.
let retiredNow = false;
export const RetireAnnounced: Story = {
  beforeEach: () => {
    retiredNow = false;
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(
        listed(
          retiredNow
            ? { ...retired, retirementOperationId: undefined }
            : environments[0],
        ),
      ),
    ),
    http.post(`${environmentRoot}/retire`, () => {
      retiredNow = true;
      return HttpResponse.json(
        { ...retired, retirementOperationId: undefined },
        { status: 201 },
      );
    }),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    // The tabs are the loaded page, not its loading fallback.
    await canvas.findByRole("tab", { name: "Settings" });
    const regions = canvas.getAllByRole("status");
    const dialog = within(await openRetireDialog(canvasElement));
    await userEvent.type(
      dialog.getByLabelText("Environment name"),
      "Development",
    );
    await userEvent.click(
      dialog.getByRole("button", { name: "Retire environment" }),
    );
    const message = await canvas.findByText("Environment retired.");
    await expect(regions).toContain(message.closest('[role="status"]'));
  },
};
// Restoring announces the outcome from a region that stays as the page becomes active again.
let restoredNow = false;
export const RestoreAnnounced: Story = {
  beforeEach: () => {
    restoredNow = false;
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(listed(restoredNow ? environments[0] : retired)),
    ),
    http.post(`${environmentRoot}/restore`, () => {
      restoredNow = true;
      return HttpResponse.json(environments[0], { status: 201 });
    }),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("heading", { name: "This environment is retired" });
    const regions = canvas.getAllByRole("status");
    await userEvent.click(
      canvas.getByRole("button", { name: "Restore environment" }),
    );
    const dialog = within(
      await within(canvasElement.ownerDocument.body).findByRole("alertdialog", {
        name: "Restore Development?",
      }),
    );
    await userEvent.type(
      dialog.getByLabelText("Environment name"),
      "Development",
    );
    await userEvent.click(
      dialog.getByRole("button", { name: "Restore environment" }),
    );
    const message = await canvas.findByText(
      "Environment restored. Nothing has been deployed.",
    );
    await expect(regions).toContain(message.closest('[role="status"]'));
  },
};
// A server's confirmation finishes the retirement, which only the page announces.
let confirmed = false;
// The refresh after the operation succeeds is held until the play function has checked the page
// in between, so the success line can be seen outside the live region before the page announces.
let releaseRefresh = () => {};
let refreshHeld = Promise.resolve();
export const RetirementCompletesOnce: Story = {
  beforeEach: () => {
    confirmed = false;
    refreshHeld = new Promise((resolve) => {
      releaseRefresh = resolve;
    });
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, async () => {
      if (!confirmed) return HttpResponse.json(listed(retiring));
      await refreshHeld;
      return HttpResponse.json(listed(retired));
    }),
    http.get(`${environmentRoot}/operations/:operationId`, () => {
      confirmed = true;
      return HttpResponse.json(operationFixture("succeeded"));
    }),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    // While the environment is still retiring, its operation's success is shown but not announced.
    await canvas.findByText("Removal from the server: finished");
    for (const region of canvas.getAllByRole("status"))
      await expect(region).not.toHaveTextContent(
        "Removal from the server: finished",
      );
    // The success refreshes the environment well before its 5 second poll would.
    releaseRefresh();
    await canvas.findByText("Environment retired.", undefined, {
      timeout: 2000,
    });
  },
};
// A retired template's owners can still remove its previews, which its retirement asked to clean
// up and which may be blocked, but nothing else about them.
const template = environments[2];
const retiredTemplate: Schema["Environment"] = {
  ...template,
  state: "retired",
  retiredAt: "2026-11-10T09:30:00Z",
  retiredBy: ownerId,
};
export const RetiredTemplatePreviews: Story = {
  args: {
    initialPath: `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${template.id}`,
  },
  parameters: scenario(
    session,
    http.get(environmentsPath, () =>
      HttpResponse.json(
        environments.map((item) =>
          item.id === template.id ? retiredTemplate : item,
        ),
      ),
    ),
    http.get(`${environmentRoot}/previews`, () =>
      HttpResponse.json([
        {
          id: "00000000-0000-4000-8000-000000000090",
          number: 42,
          head: "0123456789abcdef0123456789abcdef01234567",
          headRepository: 7,
          fork: false,
          state: "blocked",
          note: "Cleanup needs renewed owner approval or a verified agent transfer.",
          generation: 1,
          environmentId: "00000000-0000-4000-8000-000000000091",
        },
      ] satisfies Schema["PullRequestPreview"][]),
    ),
    // Retirement paused the template's policy, which an owner of an active template could resume.
    http.get(`${environmentRoot}/preview-policy`, () =>
      HttpResponse.json({
        id: "00000000-0000-4000-8000-000000000092",
        enabled: false,
        generation: 2,
        configuration: {
          baseRevisionId: "00000000-0000-4000-8000-000000000093",
          applicationId: "00000000-0000-4000-8000-000000000094",
          build: {
            connectionId: "00000000-0000-4000-8000-000000000095",
            branch: "main",
            runnerId: "00000000-0000-4000-8000-000000000096",
            context: ".",
            architecture: "amd64",
            arguments: {},
            secrets: {},
            autoBuild: true,
            autoDeploy: true,
          },
          maxActive: 3,
          maxConcurrentBuilds: 1,
          limits: { cpus: 1, memoryMiB: 512, pids: 256 },
          closeGraceHours: null,
          generatedDomains: false,
        },
      } satisfies Schema["PreviewPolicy"]),
    ),
    members,
    volumes,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("tab", { name: "Previews" }));
    const previews = within(
      await canvas.findByRole("region", { name: "Pull-request previews" }),
    );
    await expect(
      await previews.findByRole("button", { name: "Remove preview and data" }),
    ).toBeEnabled();
    await expect(
      previews.getByText(
        /This template is retired, so it creates no new previews/,
      ),
    ).toBeVisible();
    for (const name of ["Refresh preview", "Resume saved preview policy"])
      await expect(previews.queryByRole("button", { name })).toBeNull();
    await expect(previews.queryByText("Preview policy settings")).toBeNull();
  },
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
