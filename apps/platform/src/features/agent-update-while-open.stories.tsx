import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent, within } from "storybook/test";
import {
  ServerCard,
  release,
  sameFloor,
  server,
} from "../workshop/agent-update-fixtures";

// How the agent update select keeps or moves its release while the page is open, as the server
// and the catalog change.
const meta = {
  title: "Pages/Servers/Agent update while open",
  component: ServerCard,
} satisfies Meta<typeof ServerCard>;
export default meta;
type Story = StoryObj<typeof meta>;

// Another update raised the floor while the page was open: the release chosen before is now
// below it, so the choice moves to one management still sends rather than sending nothing.
export const FloorRisesWhileOpen: Story = {
  args: {
    catalog: [
      release("0.4.0", "0.2.0"),
      release("0.3.0", "0.2.0"),
      release("0.2.0", "0.0.0"),
      release("0.1.0", "0.0.0"),
    ],
    server: server("0.1.0", {
      basis: "catalog",
      minimumVersion: "0.0.0",
      versions: ["0.4.0", "0.3.0", "0.2.0"],
    }),
    later: [
      server("0.3.0", {
        basis: "catalog",
        minimumVersion: "0.2.0",
        versions: ["0.4.0"],
      }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.selectOptions(
      canvas.getByLabelText("Agent release"),
      "0.2.0",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Refresh the server" }),
    );
    await expect(
      canvas.getByRole("option", {
        name: "0.2.0 (minimum version 0.0.0, below 0.2.0)",
      }),
    ).toBeDisabled();
    await expect(canvas.getByLabelText("Agent release")).toHaveValue("0.4.0");
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(canvas.getByRole("status")).toHaveTextContent(
      "Requested 0.4.0",
    );
  },
};

// Another update moved the agent while the page was open, but the release the owner chose is
// still one management sends, and still newer than the agent's, so it stays.
export const ChoiceSurvivesARefresh: Story = {
  args: {
    catalog: sameFloor,
    server: server("0.1.0", {
      basis: "catalog",
      minimumVersion: "0.0.0",
      versions: ["0.4.0", "0.3.0", "0.2.0"],
    }),
    later: [
      server("0.2.0", {
        basis: "catalog",
        minimumVersion: "0.1.0",
        versions: ["0.4.0", "0.3.0"],
      }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = () => canvas.getByLabelText("Agent release");
    await userEvent.selectOptions(select(), "0.3.0");
    await userEvent.click(
      canvas.getByRole("button", { name: "Refresh the server" }),
    );
    await expect(select()).toHaveValue("0.3.0");
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(canvas.getByRole("status")).toHaveTextContent(
      "Requested 0.3.0",
    );
  },
};

// An owner never touched the select: what it showed first stays while management still sends
// it, rather than following a newer release management restarted with.
export const FirstOfferedStaysOnARefresh: Story = {
  args: {
    catalog: sameFloor,
    laterCatalog: [release("0.5.0", "0.1.0"), ...sameFloor],
    server: server("0.2.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.4.0", "0.3.0"],
    }),
    later: [
      server("0.2.0", {
        basis: "catalog",
        minimumVersion: "0.1.0",
        versions: ["0.5.0", "0.4.0", "0.3.0"],
      }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByLabelText("Agent release")).toHaveValue("0.4.0");
    await userEvent.click(
      canvas.getByRole("button", { name: "Refresh the server" }),
    );
    await expect(canvas.getByRole("option", { name: "0.5.0" })).toBeEnabled();
    await expect(canvas.getByLabelText("Agent release")).toHaveValue("0.4.0");
  },
};

// The owner's choice was installed, so the select moved to the newest release. When a later
// update, here a rollback made elsewhere, makes management send that release again, the select
// stays where it moved to rather than going back to the release the owner already installed.
export const InstalledChoiceDoesNotComeBack: Story = {
  args: {
    catalog: sameFloor,
    server: server("0.1.0", {
      basis: "catalog",
      minimumVersion: "0.0.0",
      versions: ["0.4.0", "0.3.0", "0.2.0"],
    }),
    later: [
      server("0.3.0", {
        basis: "catalog",
        minimumVersion: "0.1.0",
        versions: ["0.4.0", "0.2.0"],
      }),
      server("0.2.0", {
        basis: "catalog",
        minimumVersion: "0.1.0",
        versions: ["0.4.0", "0.3.0"],
      }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = () => canvas.getByLabelText("Agent release");
    const refresh = () =>
      userEvent.click(
        canvas.getByRole("button", { name: "Refresh the server" }),
      );
    await userEvent.selectOptions(select(), "0.3.0");
    await refresh();
    await expect(select()).toHaveValue("0.4.0");
    await refresh();
    await expect(canvas.getByRole("option", { name: "0.3.0" })).toBeEnabled();
    await expect(select()).toHaveValue("0.4.0");
  },
};

// The owner installed the newest release, so management now sends only older ones. The select
// leaves that release, which is gone, for no release rather than the first older one.
export const NewestInstalled: Story = {
  args: {
    catalog: sameFloor,
    server: server("0.3.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.4.0", "0.2.0"],
    }),
    later: [
      server("0.4.0", {
        basis: "catalog",
        minimumVersion: "0.1.0",
        versions: ["0.3.0", "0.2.0"],
      }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = () => canvas.getByLabelText("Agent release");
    await expect(select()).toHaveValue("0.4.0");
    await userEvent.click(
      canvas.getByRole("button", { name: "Refresh the server" }),
    );
    await expect(select()).toHaveValue("");
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(select()).toBeInvalid();
    await expect(canvas.queryByRole("status")).toBeNull();
  },
};

// The select started on 0.4.0, the newest then. Management restarted with 0.5.0 and the agent was
// updated to it elsewhere, so 0.4.0, still sent, is a rollback now. The select starts again, on no
// release, rather than offer a rollback nobody picked.
export const PassedUpgradeIsDropped: Story = {
  args: {
    catalog: sameFloor,
    laterCatalog: [release("0.5.0", "0.1.0"), ...sameFloor],
    server: server("0.2.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.4.0", "0.3.0"],
    }),
    later: [
      server("0.5.0", {
        basis: "catalog",
        minimumVersion: "0.1.0",
        versions: ["0.4.0", "0.3.0", "0.2.0"],
      }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = () => canvas.getByLabelText("Agent release");
    await expect(select()).toHaveValue("0.4.0");
    await userEvent.click(
      canvas.getByRole("button", { name: "Refresh the server" }),
    );
    await expect(canvas.getByRole("option", { name: "0.4.0" })).toBeEnabled();
    await expect(select()).toHaveValue("");
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(canvas.queryByRole("status")).toBeNull();
  },
};

// The page's release list is from before management added the agent's release, so it can't
// tell which way the owner's pick goes. The owner picks 0.4.0, an upgrade. The agent is then
// updated past it elsewhere, so 0.4.0, still sent, is a rollback now, and the select starts again
// rather than keep a pick made against the release the agent no longer runs.
export const PickWithoutTheOrderIsDroppedOncePassed: Story = {
  args: {
    catalog: [release("0.4.0", "0.1.0"), release("0.3.0", "0.1.0")],
    laterCatalog: [release("0.5.0", "0.1.0"), ...sameFloor],
    server: server("0.2.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.4.0", "0.3.0"],
    }),
    later: [
      server("0.5.0", {
        basis: "catalog",
        minimumVersion: "0.1.0",
        versions: ["0.4.0", "0.3.0", "0.2.0"],
      }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = () => canvas.getByLabelText("Agent release");
    await expect(select()).toHaveValue("");
    await userEvent.selectOptions(select(), "0.4.0");
    await userEvent.click(
      canvas.getByRole("button", { name: "Refresh the server" }),
    );
    await expect(canvas.getByRole("option", { name: "0.4.0" })).toBeEnabled();
    await expect(select()).toHaveValue("");
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(canvas.queryByRole("status")).toBeNull();
  },
};

// The owner picked 0.3.0, a rollback. Management then restarted without it, and the page's
// release list caught up before the server did, so the server still names 0.3.0. The select can't
// show a release the page doesn't list, so it starts again, on no release, rather than leave the
// browser to select the first release it lists, another rollback the owner didn't pick.
export const KeptReleaseLeavesTheList: Story = {
  args: {
    catalog: sameFloor,
    laterCatalog: sameFloor.filter((r) => r.version !== "0.3.0"),
    server: server("0.4.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.3.0", "0.2.0"],
    }),
    later: [
      server("0.4.0", {
        basis: "catalog",
        minimumVersion: "0.1.0",
        versions: ["0.3.0", "0.2.0"],
      }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = () => canvas.getByLabelText("Agent release");
    await userEvent.selectOptions(select(), "0.3.0");
    await userEvent.click(
      canvas.getByRole("button", { name: "Refresh the server" }),
    );
    await expect(select()).toHaveValue("");
    await userEvent.click(canvas.getByRole("button", { name: "Update agent" }));
    await expect(canvas.queryByRole("status")).toBeNull();
  },
};

// The owner picked a rollback. Management restarted with a newer release, but the rollback the
// owner picked stays rather than the select moving to the upgrade.
export const PickedRollbackStays: Story = {
  args: {
    catalog: sameFloor,
    laterCatalog: [release("0.5.0", "0.1.0"), ...sameFloor],
    server: server("0.4.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.3.0", "0.2.0"],
    }),
    later: [
      server("0.4.0", {
        basis: "catalog",
        minimumVersion: "0.1.0",
        versions: ["0.5.0", "0.3.0", "0.2.0"],
      }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const select = () => canvas.getByLabelText("Agent release");
    await userEvent.selectOptions(select(), "0.2.0");
    await userEvent.click(
      canvas.getByRole("button", { name: "Refresh the server" }),
    );
    await expect(canvas.getByRole("option", { name: "0.5.0" })).toBeEnabled();
    await expect(select()).toHaveValue("0.2.0");
  },
};
