import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../../api/client";
import { ShellPreview, session, workspace } from "../../workshop/app-preview";
import { scenario } from "../../workshop/fixtures";
import {
  workshopFailure,
  workspaceRoot,
} from "../../workshop/terminal-session-fixtures";
import { SecretAccess } from "./secret-access";

type AccessRecord = Schema["SecretAccessRecord"];

const accessPath = `${workspaceRoot}/secret-access`;
// Distinct leading digits keep short IDs apart.
const id = (n: number) =>
  `${String(n).padStart(8, "0")}-0000-4000-8000-000000000000`;
const record = (
  n: number,
  value: Partial<AccessRecord> & Pick<AccessRecord, "purpose" | "principal">,
): AccessRecord => ({
  id: id(900 + n),
  at: new Date(Date.parse("2026-11-10T12:00:00Z") - n * 3600000).toISOString(),
  items: [],
  itemCount: 0,
  reason: "first-release",
  outcome: "released",
  ...value,
});

// Delivery and planning give keys for whole kinds; an item without an ID stands
// for every item of its kind.
const deliveryKinds = [
  "secret-version",
  "build-secret",
  "git",
  "registry",
  "backup-destination",
  "backup-policy",
].map((kind) => ({ kind }));

const records: AccessRecord[] = [
  record(1, {
    purpose: "deliver",
    principal: {
      kind: "agent",
      serverId: id(400),
      serverName: "app-server-with-a-long-descriptive-host-name-in-frankfurt",
      instance: "key:3f9a1c27b04e5d88",
    },
    operationId: id(800),
    items: deliveryKinds,
    itemCount: deliveryKinds.length,
  }),
  record(2, {
    purpose: "seal",
    principal: {
      kind: "account",
      accountId: id(1),
      accountEmail: "alex@example.test",
    },
    items: [{ kind: "secret-version", id: id(101), name: "DATABASE_URL" }],
    itemCount: 1,
    reason: "write",
    outcome: "used",
  }),
  record(3, {
    purpose: "operate",
    principal: { kind: "system", job: "alert-delivery" },
    items: [{ kind: "alert-webhook", id: workspace.id }],
    itemCount: 1,
    reason: "operate",
    outcome: "unknown",
  }),
  record(4, {
    purpose: "inspect",
    principal: {
      kind: "automation",
      accountId: id(1),
      automationId: id(300),
      automationName: "Nightly release",
    },
    items: [{ kind: "secret-version" }, { kind: "build-secret" }],
    itemCount: 2,
    reason: "reason-added-by-a-newer-server",
    outcome: "refused",
  }),
  record(5, {
    purpose: "recover",
    principal: { kind: "installation" },
    items: [{ kind: "recovery-checkpoint" }],
    itemCount: 1,
    reason: "installation-key-maintenance",
    outcome: "used",
  }),
  record(6, {
    purpose: "deliver",
    principal: { kind: "agent", serverId: id(401) },
    items: deliveryKinds,
    itemCount: deliveryKinds.length,
    reason: "decision",
    outcome: "abandoned",
  }),
  // A former member: the API gives their email only while they are a member.
  record(7, {
    purpose: "export",
    principal: { kind: "account", accountId: id(2) },
    items: [{ kind: "secret-version" }, { kind: "git" }],
    itemCount: 2,
    reason: "export",
    outcome: "failed",
  }),
  record(8, {
    purpose: "seal",
    principal: {
      kind: "account",
      accountId: id(1),
      accountEmail: "alex@example.test",
    },
    items: [{ kind: "secret-version", id: id(102) }],
    itemCount: 1,
    reason: "write",
    outcome: "used",
  }),
];
const page = (value: AccessRecord[], next?: string) =>
  HttpResponse.json({
    records: value,
    next,
  } satisfies Schema["SecretAccessPage"]);

const meta = {
  title: "Pages/Secret access",
  component: ShellPreview,
  parameters: scenario(session),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

const accessStory = {
  render: () => (
    <main>
      <h1>{workspace.name}</h1>
      <SecretAccess workspaceId={workspace.id} />
    </main>
  ),
};

export const Records: Story = {
  ...accessStory,
  parameters: scenario(
    session,
    http.get(accessPath, () => page(records)),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByRole("table", {
        name: "Secret access records, newest first",
      }),
    ).toBeVisible();
    await expect(
      canvas.getAllByRole("cell", { name: "Delivering to a server" }),
    ).toHaveLength(2);
    await expect(
      canvas.getByText("Instance key:3f9a1c27b04e5d88"),
    ).toBeVisible();
    await expect(canvas.getByText("DATABASE_URL (Secret)")).toBeVisible();
    await expect(canvas.getByText("Secret 00000102")).toBeVisible();
    await expect(canvas.getAllByText("All secrets")).toHaveLength(4);
    await expect(canvas.getAllByText("All Git connections")).toHaveLength(3);
    await expect(canvas.getByText("All recovery checkpoints")).toBeVisible();
    await expect(canvas.queryByText(/ more$/)).not.toBeInTheDocument();
    await expect(canvas.getByRole("cell", { name: "Unknown" })).toBeVisible();
    await expect(canvas.getByText("alert-delivery")).toBeVisible();
    await expect(
      canvas.getByRole("cell", { name: "Installation maintenance" }),
    ).toBeVisible();
    await expect(
      canvas.getByRole("cell", { name: "Account 00000002" }),
    ).toBeVisible();
    await expect(
      canvas.getByRole("cell", { name: "reason-added-by-a-newer-server" }),
    ).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: "Load more" }),
    ).not.toBeInTheDocument();
  },
};

// The API lists at most 256 items and counts the rest.
export const ManyItems: Story = {
  ...accessStory,
  parameters: scenario(
    session,
    http.get(accessPath, () =>
      page([
        record(1, {
          purpose: "copy",
          principal: {
            kind: "account",
            accountId: id(1),
            accountEmail: "alex@example.test",
          },
          items: Array.from({ length: 256 }, (_, n) => ({
            kind: "secret-version",
            id: id(1000 + n),
            name: `SETTING_${n}`,
          })),
          itemCount: 260,
          reason: "copy",
          outcome: "used",
        }),
      ]),
    ),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("and 4 more")).toBeVisible();
    await expect(canvas.getByText("SETTING_255 (Secret)")).toBeVisible();
  },
};

export const Empty: Story = {
  ...accessStory,
  parameters: scenario(
    session,
    http.get(accessPath, () => page([])),
  ),
  play: async ({ canvasElement }) => {
    await expect(
      await within(canvasElement).findByText(
        "No secret access has been recorded in this workspace.",
      ),
    ).toBeVisible();
  },
};

export const LoadFailed: Story = {
  ...accessStory,
  parameters: scenario(
    session,
    http.get(accessPath, () => workshopFailure("internal", "Failed", 500)),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Try again" }),
    );
    await expect(
      canvas.getByRole("heading", { name: "Secret access" }),
    ).toHaveFocus();
    await expect(
      await canvas.findByRole("button", { name: "Try again" }),
    ).toBeEnabled();
  },
};

const [first, second, ...rest] = records;
const pagedHandler = (later: () => Response) =>
  http.get(accessPath, ({ request }) =>
    new URL(request.url).searchParams.get("before") === second.id
      ? later()
      : page([first, second], second.id),
  );

export const Paged: Story = {
  ...accessStory,
  parameters: scenario(
    session,
    pagedHandler(() => page(rest)),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Showing 2 records.")).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Load more" }));
    await expect(
      await canvas.findByRole("cell", { name: "Installation maintenance" }),
    ).toBeVisible();
    await expect(canvas.getByRole("status")).toHaveTextContent(
      "Showing 8 records.",
    );
    await expect(
      canvas.queryByRole("button", { name: "Load more" }),
    ).not.toBeInTheDocument();
    // Focus moves to the first record the page added.
    await waitFor(() =>
      expect(canvas.getAllByRole("rowheader")[2]).toHaveFocus(),
    );
  },
};

export const LoadMoreFailed: Story = {
  ...accessStory,
  parameters: scenario(
    session,
    pagedHandler(() => workshopFailure("internal", "Failed", 500)),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const more = await canvas.findByRole("button", { name: "Load more" });
    await userEvent.click(more);
    await expect(
      await canvas.findByText("More records could not be loaded."),
    ).toBeVisible();
    await expect(more).toHaveFocus();
    await expect(canvas.getAllByRole("row")).toHaveLength(3);
    // A repeated failure mounts a new alert, so it is announced again.
    const failure = canvas.getByRole("alert");
    await userEvent.click(more);
    await waitFor(() => expect(failure).not.toBeInTheDocument());
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "More records could not be loaded.",
    );
    await expect(more).toHaveFocus();
  },
};
