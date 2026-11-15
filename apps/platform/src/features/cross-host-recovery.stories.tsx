import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, within } from "storybook/test";
import type { Schema } from "../api/client";
import {
  ShellPreview,
  projectHandlers,
  session,
  workspace,
} from "../workshop/app-preview";
import { scenario } from "../workshop/fixtures";
import {
  workshopFailure,
  workspaceRoot,
} from "../workshop/terminal-session-fixtures";
import { CrossHostRecovery } from "./cross-host-recovery";

const recoveryPath = `${workspaceRoot}/cross-host-recovery`;
const checkpointAt = "2026-11-12T09:30:00Z";
const recovery = (value: Schema["CrossHostRecovery"]) =>
  scenario(
    session,
    http.get(recoveryPath, () => HttpResponse.json(value)),
    ...projectHandlers(),
  );

const meta = {
  title: "Pages/Cross-host recovery",
  component: ShellPreview,
  render: () => (
    <main>
      <h1>{workspace.name}</h1>
      <CrossHostRecovery workspaceId={workspace.id} />
    </main>
  ),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Window: Story = {
  parameters: recovery({
    checkpointAt,
    windowStart: "2026-08-14T09:30:00Z",
    shortWindow: false,
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/^Newest checkpoint: /)).toBeVisible();
    await expect(
      canvas.getByText(/^Recovery can use a management backup taken after /),
    ).toBeVisible();
    await expect(canvas.queryByText(/is shorter than/)).toBeNull();
  },
};

export const ShortWindow: Story = {
  parameters: recovery({
    checkpointAt,
    windowStart: "2026-10-23T09:30:00Z",
    shortWindow: true,
  }),
  play: async ({ canvasElement }) => {
    await expect(
      await within(canvasElement).findByText(
        /^This window is shorter than the 35 days/,
      ),
    ).toBeVisible();
  },
};

export const EarlierRelease: Story = {
  parameters: recovery({ checkpointAt, windowStart: null, shortWindow: false }),
  play: async ({ canvasElement }) => {
    await expect(
      await within(canvasElement).findByText(/carries all applied history/),
    ).toBeVisible();
  },
};

export const NoCheckpoint: Story = {
  parameters: recovery({
    checkpointAt: null,
    windowStart: null,
    shortWindow: false,
  }),
  play: async ({ canvasElement }) => {
    await expect(
      await within(canvasElement).findByText(/^No checkpoint yet\./),
    ).toBeVisible();
  },
};

export const LoadFailure: Story = {
  parameters: scenario(
    session,
    http.get(recoveryPath, () =>
      workshopFailure("internal", "The request could not be completed.", 500),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    await expect(
      await within(canvasElement).findByRole("alert"),
    ).toHaveTextContent("Cross-host recovery could not be loaded.");
  },
};
