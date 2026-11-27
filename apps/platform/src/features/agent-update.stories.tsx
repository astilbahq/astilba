import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { useState } from "react";
import { expect, userEvent, within } from "storybook/test";
import type { Schema } from "../api/client";
import { useAction } from "../ui";
import { AgentUpdate } from "./agent-update";

const release = (
  version: string,
  minimumVersion: string,
  arch: Schema["AgentRelease"]["arch"] = "arm64",
): Schema["AgentRelease"] => ({
  version,
  minimumVersion,
  arch,
  protocol: 1,
  size: 1,
  sha256: `${version}-${arch}`.padEnd(64, "0"),
  commit: "0".repeat(40),
  signature: "",
});
// Management lists the catalog newest first.
const releases = [
  release("0.3.1", "0.0.0", "amd64"),
  release("0.3.0", "0.3.0"),
  release("0.2.1", "0.1.0"),
  release("0.2.0", "0.1.0"),
  release("0.1.0", "0.0.0"),
];
const server = (
  version: string,
  agentUpdates?: Schema["AgentUpdates"],
): Schema["Server"] => ({
  id: "00000000-0000-4000-8000-000000000330",
  name: "edge-1",
  role: "deployment",
  status: "online",
  lastSeen: "2026-11-26T09:00:00Z",
  rotationRequested: false,
  preflight: null,
  capabilities: {
    protocol: 1,
    version,
    os: "linux",
    arch: "arm64",
    docker: true,
  },
  agentUpdates,
});

// The panel sits in a server's card on the Servers page, and reports the release it would send.
// later stands in for the page's next refreshes of the server, each after its agent changed, and
// laterCatalog for the catalog after management restarted with another one.
function ServerCard({
  server,
  later = [],
  catalog = releases,
  laterCatalog = catalog,
}: {
  server: Schema["Server"];
  later?: Schema["Server"][];
  catalog?: Schema["AgentRelease"][];
  laterCatalog?: Schema["AgentRelease"][];
}) {
  const action = useAction();
  const [sent, setSent] = useState<string>();
  const [refreshes, setRefreshes] = useState(0);
  return (
    <main>
      <h1>Servers</h1>
      <article aria-label={server.name}>
        <h2>{server.name}</h2>
        <AgentUpdate
          server={refreshes ? later[refreshes - 1] : server}
          releases={refreshes ? laterCatalog : catalog}
          action={action}
          busy={false}
          onUpdate={setSent}
        />
        {refreshes < later.length && (
          <button type="button" onClick={() => setRefreshes(refreshes + 1)}>
            Refresh the server
          </button>
        )}
        {sent !== undefined && <p role="status">Requested {sent}</p>}
      </article>
    </main>
  );
}

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

const sameFloor = [
  release("0.4.0", "0.1.0"),
  release("0.3.0", "0.1.0"),
  release("0.2.0", "0.1.0"),
  release("0.1.0", "0.0.0"),
];

// Another update moved the agent while the page was open, but the release the owner chose is
// still one management sends, so it stays, even though it is now a rollback.
export const ChoiceSurvivesARefresh: Story = {
  args: {
    catalog: sameFloor,
    server: server("0.2.0", {
      basis: "catalog",
      minimumVersion: "0.1.0",
      versions: ["0.4.0", "0.3.0"],
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
    await expect(canvas.getByRole("option", { name: "0.2.0" })).toBeEnabled();
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

// Management takes no update for a revoked server or one awaiting a transfer, which holds no
// agent credential, so it sends no agent updates for them, and there is no form, though each
// keeps the capabilities its agent last reported.
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

export const AwaitingTransfer: Story = {
  args: {
    server: { ...server("0.2.0"), status: "offline", transferRequired: true },
  },
  play: RevokedServer.play,
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
