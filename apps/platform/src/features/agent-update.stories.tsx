import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent, within } from "storybook/test";
import {
  ServerCard,
  release,
  sameFloor,
  server,
} from "../workshop/agent-update-fixtures";

// Which releases the Servers page offers an agent, and the release its select starts on.
const meta = {
  title: "Pages/Servers/Agent update",
  component: ServerCard,
} satisfies Meta<typeof ServerCard>;
export default meta;
type Story = StoryObj<typeof meta>;

// Management read the floor from the catalog entry for the release the agent runs. A release
// below it stays listed, disabled with the reason, and the newest release it sends is chosen.
export const BelowTheFloor: Story = {
  args: {
    server: server("0.2.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.3.0", "0.2.1"],
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = canvas.getByLabelText("Agent release");
    await expect(select).toHaveAccessibleDescription(
      "This agent installs only releases whose minimum version is at least 0.1.0, that of the release it runs.",
    );
    await expect(select).toHaveValue("0.3.0");
    const below = canvas.getByRole("option", {
      name: "0.1.0 (minimum version 0.0.0, below 0.1.0)",
    });
    await expect(below).toBeDisabled();
    await expect(canvas.getByRole("option", { name: "0.2.1" })).toBeEnabled();
    // Neither the running release nor another architecture's is listed.
    await expect(canvas.getAllByRole("option")).toHaveLength(3);
    await userEvent.selectOptions(select, "0.2.1");
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(canvas.getByRole("status")).toHaveTextContent(
      "Requested 0.2.1",
    );
  },
};

// The release the agent runs isn't in the catalog, so its version stands in for the floor.
export const ReleaseNotInTheCatalog: Story = {
  args: {
    server: server("0.2.5", {
      basis: "version",
      minimumVersion: "0.2.5",
      versions: ["0.3.0"],
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = canvas.getByLabelText("Agent release");
    await expect(select).toHaveAccessibleDescription(
      "Release 0.2.5, which this agent runs, isn't in the release catalog, so management can't read its minimum version. Management offers only releases whose minimum version is at least 0.2.5, which the agent installs whatever its own minimum version. Add this release's signed manifest to the catalog so management can read its minimum version, or update the agent with the local procedure.",
    );
    await expect(select).toHaveValue("0.3.0");
    for (const name of [
      "0.2.1 (minimum version 0.1.0, below 0.2.5)",
      "0.2.0 (minimum version 0.1.0, below 0.2.5)",
      "0.1.0 (minimum version 0.0.0, below 0.2.5)",
    ])
      await expect(canvas.getByRole("option", { name })).toBeDisabled();
  },
};

// Version order isn't the order of manifest names, where 0.10.0 comes before 0.9.0: management
// lists 0.10.0 first, and the select starts on it, so its default is never a downgrade.
export const NewestFirst: Story = {
  args: {
    catalog: [
      release("0.10.0", "0.1.0"),
      release("0.9.0", "0.1.0"),
      release("0.2.0", "0.1.0"),
    ],
    server: server("0.2.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.10.0", "0.9.0"],
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByLabelText("Agent release")).toHaveValue("0.10.0");
    await expect(
      canvas.getAllByRole("option").map((o) => o.textContent),
    ).toEqual(["0.10.0", "0.9.0"]);
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(canvas.getByRole("status")).toHaveTextContent(
      "Requested 0.10.0",
    );
  },
};

// The agent runs the newest release, so management sends only older ones. The select starts on
// none of them, and the form sends nothing until the owner picks the rollback they want.
export const OnlyOlderReleases: Story = {
  args: {
    catalog: sameFloor,
    server: server("0.4.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.3.0", "0.2.0"],
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = canvas.getByLabelText("Agent release");
    await expect(select).toHaveValue("");
    await expect(
      canvas.getByRole("option", {
        name: "Choose a release older than 0.4.0, which this agent runs",
      }),
    ).toBeDisabled();
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(select).toBeInvalid();
    await expect(canvas.queryByRole("status")).toBeNull();
    await userEvent.selectOptions(select, "0.3.0");
    await expect(select).toBeValid();
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(canvas.getByRole("status")).toHaveTextContent(
      "Requested 0.3.0",
    );
  },
};

// Management restarted with a catalog that now holds the agent's release, but the page's release
// list is from before, so it can't tell whether what management sends is newer or older. The
// select starts on no release rather than on one that may be a rollback.
export const ReleaseListOlderThanTheServer: Story = {
  args: {
    catalog: [release("0.9.0", "0.1.0")],
    server: server("0.10.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.9.0"],
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByLabelText("Agent release")).toHaveValue("");
    await expect(
      canvas.getByRole("option", { name: "Choose a release" }),
    ).toBeDisabled();
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(canvas.queryByRole("status")).toBeNull();
  },
};

// Management takes no update for a revoked server, or for one awaiting a transfer or a new
// enrollment, so it sends no agent updates for them and there is no form, though each keeps the
// capabilities its agent last reported.
export const RevokedServer: Story = {
  args: { server: { ...server("0.2.0"), status: "revoked" } },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.queryByLabelText("Agent release")).toBeNull();
    await expect(
      canvas.queryByRole("button", { name: "Update agent" }),
    ).toBeNull();
  },
};

// The agent runs the newest release, whose floor no other release in the catalog meets.
export const NoReleaseMeetsTheFloor: Story = {
  args: {
    server: server("0.3.0", {
      basis: "catalog",
      minimumVersion: "0.3.0",
      versions: [],
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      canvas.getByText(
        "This agent installs only releases whose minimum version is at least 0.3.0, that of the release it runs. No other release in the catalog has a minimum version that high, so management offers none.",
      ),
    ).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: "Update agent" }),
    ).toBeNull();
  },
};

// With no valid version, nothing bounds the agent's floor, so nothing is offered.
export const NoReleaseVersion: Story = {
  args: { server: server("fixture", { basis: "unknown", versions: [] }) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      canvas.getByText(
        "This agent reports no release version, so management can't tell which releases it installs and offers none. Update it with the local procedure.",
      ),
    ).toBeVisible();
    await expect(canvas.queryByLabelText("Agent release")).toBeNull();
    await expect(
      canvas.queryByRole("button", { name: "Update agent" }),
    ).toBeNull();
  },
};
