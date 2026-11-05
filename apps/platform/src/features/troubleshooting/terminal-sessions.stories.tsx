import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../../api/client";
import {
  ShellPreview,
  environments,
  examples,
  projectHandlers,
  session,
  workspace,
} from "../../workshop/app-preview";
import { environmentRoot } from "../../workshop/environment-fixtures";
import { scenario } from "../../workshop/fixtures";
import {
  nameHandlers as names,
  otherSession as others,
  ownSession as own,
  workshopFailure,
  workspaceRoot,
} from "../../workshop/terminal-session-fixtures";

const policyPath = `${environmentRoot}/troubleshooting`;
const sessionsPath = `${policyPath}/sessions`;
const closePath = `${workspaceRoot}/troubleshooting-sessions/:sessionId/close`;
const environment = environments[0];
const policy = (change: Partial<Schema["TroubleshootingPolicy"]> = {}) =>
  http.get(policyPath, () =>
    HttpResponse.json({
      enabled: true,
      ownersOnly: false,
      generation: 3,
      available: true,
      ...change,
    } satisfies Schema["TroubleshootingPolicy"]),
  );
const open = http.get(sessionsPath, () => HttpResponse.json([own, others]));
const operatorSession = {
  ...session,
  workspaces: [{ ...workspace, role: "operator" as const }],
};
// Longer than the open-session poll.
const pollWait = 7000;

async function openSettings(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.click(await canvas.findByRole("tab", { name: "Settings" }));
  return canvas;
}

const meta = {
  title: "Pages/Terminal sessions",
  component: ShellPreview,
  args: {
    initialPath: `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${environment.id}`,
  },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

export const SettingOff: Story = {
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    await expect(
      await canvas.findByRole("checkbox", {
        name: "Allow terminal sessions in this environment",
      }),
    ).not.toBeChecked();
    await expect(
      canvas.getByRole("checkbox", { name: "Only owners can open sessions" }),
    ).toBeDisabled();
    await expect(
      canvas.getByRole("button", { name: "Save setting" }),
    ).toBeDisabled();
  },
};

export const InstallationNotEnabled: Story = {
  parameters: scenario(
    session,
    policy({ enabled: false, available: false }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    await expect(
      await canvas.findByText(/This installation has not enabled terminal/),
    ).toBeVisible();
  },
};

export const SettingLoadFailed: Story = {
  parameters: scenario(
    session,
    http.get(policyPath, () => workshopFailure("internal", "Failed", 500)),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    await expect(
      await canvas.findByText(
        "The terminal session setting could not be loaded.",
      ),
    ).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Try again" }));
    await expect(
      canvas.getByRole("heading", { name: "Access in this environment" }),
    ).toHaveFocus();
    await expect(
      await canvas.findByRole("button", { name: "Try again" }),
    ).toBeEnabled();
  },
};

export const OpenSessionsLoadFailed: Story = {
  parameters: scenario(
    session,
    policy(),
    http.get(sessionsPath, () => workshopFailure("internal", "Failed", 500)),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    await expect(
      await canvas.findByText("Open sessions could not be loaded."),
    ).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Try again" }));
    await expect(
      canvas.getByRole("heading", { name: "Open sessions" }),
    ).toHaveFocus();
  },
};

let refreshes = 0;
export const OpenSessionsRefreshFailed: Story = {
  beforeEach: () => {
    refreshes = 0;
  },
  parameters: scenario(
    session,
    policy(),
    http.get(sessionsPath, () =>
      refreshes++ === 0
        ? HttpResponse.json([own])
        : workshopFailure("internal", "Failed", 500),
    ),
    ...names,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    await expect(
      await canvas.findByText(
        /could not be refreshed/,
        {},
        { timeout: pollWait },
      ),
    ).toBeVisible();
    await expect(
      canvas.getByRole("button", { name: "End session on api, instance 1" }),
    ).toBeEnabled();
  },
};

export const OpenSessionsForOwner: Story = {
  parameters: scenario(session, policy(), open, ...names, ...projectHandlers()),
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    await expect(
      await canvas.findByRole("button", {
        name: "End session on api, instance 1",
      }),
    ).toBeEnabled();
    await expect(
      await canvas.findByRole("button", {
        name: "End session on worker, instance 2",
      }),
    ).toBeEnabled();
    await expect(await canvas.findByText("Sam")).toBeVisible();
  },
};

export const OpenSessionsForOperator: Story = {
  parameters: scenario(operatorSession, open, ...names, ...projectHandlers()),
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    await expect(
      await canvas.findByRole("button", {
        name: "End session on api, instance 1",
      }),
    ).toBeEnabled();
    await expect(
      canvas.queryByRole("button", { name: /End session on worker/ }),
    ).not.toBeInTheDocument();
    await expect(
      canvas.queryByRole("button", { name: /End all sessions/ }),
    ).not.toBeInTheDocument();
    await expect(
      canvas.queryByRole("checkbox", { name: /Allow terminal sessions/ }),
    ).not.toBeInTheDocument();
  },
};

export const EndAllConfirmation: Story = {
  parameters: OpenSessionsForOwner.parameters,
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", {
        name: "End all sessions in this workspace",
      }),
    );
    const dialog = within(canvasElement.ownerDocument.body);
    await expect(
      await dialog.findByText(/every environment of this workspace/),
    ).toBeVisible();
  },
};

let endedElsewhere = false;
export const SessionEndsWhileConfirming: Story = {
  beforeEach: () => {
    endedElsewhere = false;
  },
  parameters: scenario(
    session,
    policy(),
    http.get(sessionsPath, () =>
      HttpResponse.json(endedElsewhere ? [others] : [own, others]),
    ),
    http.post(closePath, () => workshopFailure("not_found", "Not found", 404)),
    ...names,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", {
        name: "End session on api, instance 1",
      }),
    );
    const dialog = within(canvasElement.ownerDocument.body);
    const confirm = await dialog.findByRole("button", { name: "End session" });
    endedElsewhere = true;
    await waitFor(
      () =>
        expect(canvas.queryByText("api, instance 1")).not.toBeInTheDocument(),
      { timeout: pollWait },
    );
    await userEvent.click(confirm);
    await expect(
      await canvas.findByText("That session had already ended."),
    ).toBeVisible();
    await waitFor(() =>
      expect(
        canvas.getByRole("heading", { name: "Open sessions" }),
      ).toHaveFocus(),
    );
  },
};

let expired = false;
export const RowDisappearsWhileFocused: Story = {
  beforeEach: () => {
    expired = false;
  },
  parameters: scenario(
    session,
    policy(),
    http.get(sessionsPath, () =>
      HttpResponse.json(expired ? [others] : [own, others]),
    ),
    ...names,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    (
      await canvas.findByRole("button", {
        name: "End session on api, instance 1",
      })
    ).focus();
    expired = true;
    await waitFor(
      () =>
        expect(
          canvas.getByRole("heading", { name: "Open sessions" }),
        ).toHaveFocus(),
      { timeout: pollWait },
    );
  },
};

const mfaFree = {
  ...session,
  user: { ...session.user!, mfaEnabled: false },
};
const offPolicy: Schema["TroubleshootingPolicy"] = {
  enabled: false,
  ownersOnly: false,
  generation: 0,
  available: true,
};
let identityConfirmed = false;
let savedPolicy = offPolicy;
const identityStory = {
  beforeEach: () => {
    identityConfirmed = false;
    savedPolicy = offPolicy;
  },
  parameters: scenario(
    mfaFree,
    http.get(policyPath, () => HttpResponse.json(savedPolicy)),
    http.put(policyPath, async ({ request }) => {
      if (!identityConfirmed)
        return workshopFailure(
          "recent_auth_required",
          "Confirm your credentials before this action.",
          403,
        );
      const body =
        (await request.json()) as Schema["SaveTroubleshootingPolicy"];
      savedPolicy = {
        ...body,
        generation: body.generation + 1,
        available: true,
      };
      return HttpResponse.json(savedPolicy);
    }),
    http.post("/api/v1/auth/reauthenticate", () => {
      identityConfirmed = true;
      return HttpResponse.json(mfaFree);
    }),
    ...projectHandlers(),
  ),
};

async function requestIdentity(canvasElement: HTMLElement) {
  const canvas = await openSettings(canvasElement);
  const allow = await canvas.findByRole("checkbox", {
    name: "Allow terminal sessions in this environment",
  });
  await userEvent.click(allow);
  await userEvent.click(canvas.getByRole("button", { name: "Save setting" }));
  await expect(
    await canvas.findByRole("heading", {
      level: 4,
      name: "Confirm your identity",
    }),
  ).toHaveFocus();
  return canvas;
}

export const ConfirmsIdentityBeforeEnabling: Story = {
  ...identityStory,
  play: async ({ canvasElement }) => {
    const canvas = await requestIdentity(canvasElement);
    await userEvent.type(
      canvas.getByLabelText("Current password"),
      "workshop password",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Confirm identity" }),
    );
    await expect(
      await canvas.findByText("Terminal session setting saved."),
    ).toBeVisible();
    await expect(
      canvas.getByRole("heading", { name: "Access in this environment" }),
    ).toHaveFocus();
    await expect(
      canvas.getByRole("checkbox", {
        name: "Allow terminal sessions in this environment",
      }),
    ).toBeChecked();
  },
};

export const CancelsIdentityConfirmation: Story = {
  ...identityStory,
  play: async ({ canvasElement }) => {
    const canvas = await requestIdentity(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Cancel" }));
    await expect(
      canvas.getByRole("heading", { name: "Access in this environment" }),
    ).toHaveFocus();
    await expect(
      canvas.getByRole("checkbox", {
        name: "Allow terminal sessions in this environment",
      }),
    ).toBeChecked();
    await expect(
      canvas.getByRole("button", { name: "Save setting" }),
    ).toBeEnabled();
  },
};

let conflictGeneration = 3;
export const GenerationConflict: Story = {
  beforeEach: () => {
    conflictGeneration = 3;
  },
  parameters: scenario(
    session,
    http.get(policyPath, () =>
      HttpResponse.json({
        enabled: true,
        ownersOnly: true,
        generation: conflictGeneration,
        available: true,
      } satisfies Schema["TroubleshootingPolicy"]),
    ),
    http.put(policyPath, () => {
      conflictGeneration = 4;
      return workshopFailure(
        "conflict",
        "The resource changed. Review the current version before saving.",
        409,
      );
    }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = await openSettings(canvasElement);
    const restriction = await canvas.findByRole("checkbox", {
      name: "Only owners can open sessions",
    });
    await userEvent.click(restriction);
    await userEvent.click(canvas.getByRole("button", { name: "Save setting" }));
    await expect(
      await canvas.findByText(/Your choices are kept here/),
    ).toBeVisible();
    await expect(restriction).not.toBeChecked();
    // The alert can appear just before the failed save stops being busy.
    await expect(
      await canvas.findByRole("button", { name: "Save setting" }),
    ).toBeDisabled();
    const loadCurrent = canvas.getByRole("button", {
      name: "Load current setting",
    });
    await waitFor(() => expect(loadCurrent).toHaveFocus());
    await userEvent.click(loadCurrent);
    await expect(restriction).toBeChecked();
    await expect(
      canvas.getByRole("heading", { name: "Access in this environment" }),
    ).toHaveFocus();
  },
};
