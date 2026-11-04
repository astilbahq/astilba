import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, within } from "storybook/test";
import { ApiError, type Schema } from "../../api/client";
import {
  ShellPreview,
  pageBoot,
  projectHandlers,
  session,
  workspace,
} from "../../workshop/app-preview";
import { scenario } from "../../workshop/fixtures";
import {
  nameHandlers as names,
  otherSession as others,
  ownSession as own,
  workshopFailure,
  workspaceRoot,
} from "../../workshop/terminal-session-fixtures";
import { SessionHistory } from "./session-history";
import { SessionErrorNotice } from "./session-text";

const historyPath = `${workspaceRoot}/troubleshooting-sessions`;
const closed = (
  value: Schema["TroubleshootingSession"],
  id: string,
  minutes: number,
  closeReason: string,
): Schema["TroubleshootingSession"] => ({
  ...value,
  id: `00000000-0000-4000-8000-00000000070${id}`,
  state: "closed",
  closedAt: new Date(
    Date.parse(value.createdAt) + minutes * 60000,
  ).toISOString(),
  closeReason,
});
const history = [
  own,
  closed(others, "1", 0, "closed"),
  closed(own, "2", 1, "closed by owner"),
  closed(others, "3", 12, "environment setting changed"),
  closed(own, "4", 60, "expired"),
  closed(others, "5", 3, "management recovery"),
  closed(own, "6", 3, "reason added by a newer server"),
  {
    ...others,
    id: "00000000-0000-4000-8000-000000000707",
    deadlineAt: own.createdAt,
  },
  {
    ...closed(own, "8", 2, "closed"),
    actorId: "00000000-0000-4000-8000-000000000999",
    applicationId: "00000000-0000-4000-8000-000000000998",
    serverId: "00000000-0000-4000-8000-000000000997",
    environmentId: "00000000-0000-4000-8000-000000000996",
  },
];

const meta = {
  title: "Pages/Terminal session history",
  component: ShellPreview,
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

const historyStory = {
  render: () => (
    <main>
      <h1>{workspace.name}</h1>
      <SessionHistory workspaceId={workspace.id} />
    </main>
  ),
};

export const History: Story = {
  ...historyStory,
  parameters: scenario(
    session,
    http.get(historyPath, () => HttpResponse.json(history)),
    ...names,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByRole("cell", { name: "Under a minute" }),
    ).toBeVisible();
    await expect(
      canvas.getByRole("cell", { name: "Ended by an owner" }),
    ).toBeVisible();
    await expect(
      canvas.getByRole("cell", { name: "reason added by a newer server" }),
    ).toBeVisible();
    await expect(canvas.getByRole("cell", { name: "Expired" })).toBeVisible();
    await expect(
      await canvas.findAllByRole(
        "rowheader",
        { name: /api, instance 1/ },
        pageBoot,
      ),
    ).not.toHaveLength(0);
    await expect(
      await canvas.findByRole("rowheader", { name: /Service 00000000/ }),
    ).toBeVisible();
  },
};

export const HistoryEmpty: Story = {
  ...historyStory,
  parameters: scenario(
    session,
    http.get(historyPath, () => HttpResponse.json([])),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    await expect(
      await within(canvasElement).findByText(
        "No terminal sessions have been opened in this workspace.",
      ),
    ).toBeVisible();
  },
};

export const HistoryLoadFailed: Story = {
  ...historyStory,
  parameters: scenario(
    session,
    http.get(historyPath, () => workshopFailure("internal", "Failed", 500)),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Try again" }),
    );
    await expect(
      canvas.getByRole("heading", { name: "Terminal session history" }),
    ).toHaveFocus();
    await expect(
      await canvas.findByRole("button", { name: "Try again" }),
    ).toBeEnabled();
  },
};

const refused = (reason: string) =>
  new ApiError(
    {
      code: "troubleshooting_refused",
      message: "Server copy",
      fields: [reason],
      requestId: "workshop",
    },
    409,
  );

export const RefusalReasons: Story = {
  render: () => (
    <main>
      {[
        "installation",
        "environment",
        "not_deployed",
        "agent_update",
        "runtime",
        "host_not_ready",
        "host_opt_in",
        "limit",
        "reason_added_by_a_newer_server",
      ].map((reason) => (
        <SessionErrorNotice key={reason} error={refused(reason)} />
      ))}
    </main>
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      canvas.getByText(
        "This server’s owner has not enabled terminal sessions on the host.",
      ),
    ).toBeVisible();
    await expect(canvas.getByText("Server copy")).toBeVisible();
    await expect(canvas.queryByText(/host_opt_in/)).not.toBeInTheDocument();
  },
};
