import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { ErrorNotice, useAction } from "../../ui";
import { member, scenario } from "../../workshop/fixtures";
import { PausedBuildTargets, PausedSource } from "./automation-approval";

const path: EnvironmentPath = {
  workspaceId: "00000000-0000-4000-8000-000000000002",
  projectId: "00000000-0000-4000-8000-000000000003",
  environmentId: "00000000-0000-4000-8000-000000000004",
};
const root = `/api/v1/workspaces/${path.workspaceId}/projects/${path.projectId}/environments/${path.environmentId}`;
const source: Schema["GitSourceStatus"] = {
  approval: "approverCredentials",
  clean: true,
  configuration: {
    connectionId: "00000000-0000-4000-8000-000000000005",
    branch: "main",
    runnerId: "00000000-0000-4000-8000-000000000006",
    entrypoints: ["compose.yaml"],
    paths: ["compose.yaml"],
    autoDeploy: true,
  },
  importedCommit: "a".repeat(40),
  importedRevisionId: null,
  pendingCommit: "",
  status: "ready",
  sourceHash: "f".repeat(64),
  submission: null,
};
const target = (
  id: string,
  name: string,
  approval: Schema["PolicyApproval"],
  automaticDeploy: boolean,
  runnable = false,
): Schema["ConfiguredBuildTarget"] => ({
  applicationId: `00000000-0000-4000-8000-00000000010${id}`,
  baseRevisionId: "00000000-0000-4000-8000-000000000007",
  name,
  branch: "main",
  builder: "dockerfile",
  targetHash: id.repeat(64),
  automaticBuild: true,
  automaticDeploy,
  approval,
  runnable,
});
const targets = [
  target("1", "web", "approverRemoved", true),
  target("2", "worker", "approverRole", false),
  target("3", "api", "current", true, true),
  target("4", "jobs", "current", false),
];

// What the workshop server was asked to approve, reset before each story.
let approvals: unknown[] = [];

function Paused({ owner, write }: { owner: boolean; write: boolean }) {
  const action = useAction();
  return (
    <main id="main">
      <ErrorNotice error={action.error} />
      <PausedSource
        path={path}
        source={source}
        owner={owner}
        action={action}
        approved={async () => {}}
        refresh={async () => {}}
      />
      <PausedBuildTargets
        path={path}
        targets={targets}
        write={write}
        owner={owner}
        action={action}
        approved={async () => {}}
        refresh={async () => {}}
      />
    </main>
  );
}

const meta = {
  title: "Patterns/Paused automation",
  component: Paused,
  args: { owner: true, write: true },
  parameters: scenario(
    member,
    http.post(`${root}/source/approval`, async ({ request }) => {
      approvals.push(await request.json());
      return new HttpResponse(null, { status: 204 });
    }),
    http.post(
      `${root}/build-targets/:applicationId/approval`,
      async ({ request, params }) => {
        approvals.push({
          applicationId: params.applicationId,
          ...((await request.json()) as object),
        });
        return new HttpResponse(null, { status: 204 });
      },
    ),
  ),
  beforeEach: () => {
    approvals = [];
  },
} satisfies Meta<typeof Paused>;
export default meta;
type Story = StoryObj<typeof meta>;

// An owner sees why each piece of automatic work is paused and approves it again as theirs,
// keeping its configuration, and which builds automation credentials can't run. Work that runs
// on a current approval is not listed.
export const Owner: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      canvas.getByText(/changed their password, two-factor settings/),
    ).toBeVisible();
    await expect(canvas.getByText(/has left this workspace/)).toBeVisible();
    await expect(canvas.queryByText(/^api:/)).toBeNull();
    await expect(
      canvas.getByText(/jobs: automation credentials can’t run this build/),
    ).toBeVisible();
    await userEvent.click(
      canvas.getByRole("button", { name: "Approve as mine and resume" }),
    );
    await waitFor(() => expect(approvals).toHaveLength(1));
    await waitFor(() =>
      expect(
        canvas.getByRole("button", { name: "Approve web as mine and resume" }),
      ).toBeEnabled(),
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Approve web as mine and resume" }),
    );
    await waitFor(() =>
      expect(approvals).toEqual([
        { sourceHash: "f".repeat(64) },
        {
          applicationId: targets[0]!.applicationId,
          targetHash: "1".repeat(64),
        },
      ]),
    );
  },
};

// An operator can approve automatic building, but a source or a target that deploys needs an owner.
export const Operator: Story = {
  args: { owner: false, write: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      canvas.queryByRole("button", { name: "Approve as mine and resume" }),
    ).toBeNull();
    await expect(
      canvas.queryByRole("button", { name: "Approve web as mine and resume" }),
    ).toBeNull();
    await expect(
      canvas.getByRole("button", { name: "Approve worker as mine and resume" }),
    ).toBeVisible();
    await expect(
      canvas.getAllByText("An owner can approve it again."),
    ).toHaveLength(2);
    // Only owners can make a build runnable by automation, so others aren't told about it.
    await expect(
      canvas.queryByText(/automation credentials can’t run/),
    ).toBeNull();
  },
};
