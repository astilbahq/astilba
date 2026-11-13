import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { useState } from "react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema, Session } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { member, scenario } from "../../workshop/fixtures";
import { ChangeSource, SourceFailure } from "./source-binding";

const path: EnvironmentPath = {
  workspaceId: "00000000-0000-4000-8000-000000000002",
  projectId: "00000000-0000-4000-8000-000000000003",
  environmentId: "00000000-0000-4000-8000-000000000004",
};
const workspaceRoot = `/api/v1/workspaces/${path.workspaceId}`;
const root = `${workspaceRoot}/projects/${path.projectId}/environments/${path.environmentId}`;
const revisionId = "00000000-0000-4000-8000-000000000007";
const connectionId = "00000000-0000-4000-8000-000000000005";
const runnerId = "00000000-0000-4000-8000-000000000006";
const source: Schema["GitSourceStatus"] = {
  approval: "current",
  clean: true,
  configuration: {
    connectionId,
    branch: "main",
    runnerId,
    entrypoints: ["compose.yaml"],
    paths: ["compose.yaml", "config/app.env"],
    autoDeploy: false,
  },
  importedCommit: "a".repeat(40),
  importedRevisionId: revisionId,
  pendingCommit: "",
  status: "ready",
  sourceHash: "f".repeat(64),
  submission: null,
  failure: {
    commit: "e".repeat(40),
    reason:
      'Commit eeeeeeeeeeee doesn\'t have the file "compose.yaml", which the source selects. If it was renamed or removed, bind the source again with the files the branch holds now.',
    failedAt: "2026-11-13T10:00:00Z",
  },
};
// The owner has confirmed their identity recently, so neither change asks again.
const owner: Session = { ...member, recentAuth: true };

// What the workshop server was asked to do, reset before each story.
let requests: { method: string; body: unknown }[] = [];

function Panel() {
  const [said, setSaid] = useState("");
  return (
    <main id="main">
      <SourceFailure failure={source.failure} />
      <ChangeSource
        path={path}
        revisionId={revisionId}
        source={source}
        dirty={false}
        changed={async (message) => setSaid(message)}
      />
      {said && <p role="status">{said}</p>}
    </main>
  );
}

const meta = {
  title: "Patterns/Git source binding",
  component: Panel,
  parameters: scenario(
    owner,
    http.get(`${workspaceRoot}/git-connections`, () =>
      HttpResponse.json([
        {
          id: connectionId,
          name: "App",
          provider: "github",
          baseUrl: "https://api.github.com",
          repository: "team/app",
        },
      ] as Schema["GitConnection"][]),
    ),
    http.get(`${workspaceRoot}/servers`, () =>
      HttpResponse.json([
        {
          id: runnerId,
          name: "builder",
          role: "build",
          status: "online",
          capabilities: { build: true, arch: "arm64" },
        },
      ] as Schema["Server"][]),
    ),
    http.put(`${root}/source`, async ({ request }) => {
      requests.push({ method: "PUT", body: await request.json() });
      return HttpResponse.json(
        {
          id: "00000000-0000-4000-8000-000000000008",
          serverId: runnerId,
          environmentId: path.environmentId,
          generation: 2,
          state: "queued",
          kind: "pipeline",
          cancelRequested: false,
          note: "",
          steps: [],
          createdAt: "2026-11-13T10:01:00Z",
        } satisfies Schema["Operation"],
        { status: 202 },
      );
    }),
    http.delete(`${root}/source`, () => {
      requests.push({ method: "DELETE", body: null });
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  beforeEach: () => {
    requests = [];
  },
} satisfies Meta<typeof Panel>;
export default meta;
type Story = StoryObj<typeof meta>;

// A selected file renamed upstream stops automatic imports and says which file. The owner binds
// the source again with the new name, keeping everything else as it was.
export const RebindRenamedFile: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      canvas.getByText(/doesn't have the file "compose.yaml"/),
    ).toBeVisible();
    await userEvent.click(canvas.getByText("Change or remove the Git source"));
    const paths = canvas.getByRole("textbox", {
      name: "Selected source files, one path per line",
    });
    await waitFor(() =>
      expect(canvas.getByRole("option", { name: /builder/ })).toBeTruthy(),
    );
    await userEvent.clear(paths);
    await userEvent.type(paths, "docker-compose.yaml{enter}config/app.env");
    const entrypoints = canvas.getByRole("textbox", {
      name: "Compose entrypoints, one path per line",
    });
    await userEvent.clear(entrypoints);
    await userEvent.type(entrypoints, "docker-compose.yaml");
    await userEvent.click(
      canvas.getByRole("button", { name: "Bind again and import" }),
    );
    await waitFor(() =>
      expect(requests).toEqual([
        {
          method: "PUT",
          body: {
            baseRevisionId: revisionId,
            configuration: {
              connectionId,
              branch: "main",
              runnerId,
              entrypoints: ["docker-compose.yaml"],
              paths: ["docker-compose.yaml", "config/app.env"],
              autoDeploy: false,
            },
          },
        },
      ]),
    );
    await expect(await canvas.findByRole("status")).toHaveTextContent(
      /Source changed/,
    );
  },
};

// Removing the source asks first, and keeping it changes nothing.
export const RemoveSource: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(canvas.getByText("Change or remove the Git source"));
    await userEvent.click(
      canvas.getByRole("button", { name: "Remove Git source" }),
    );
    const dialog = await body.findByRole("alertdialog", {
      name: "Remove the Git source?",
    });
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Keep source" }),
    );
    await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull());
    await expect(requests).toEqual([]);
    await userEvent.click(
      canvas.getByRole("button", { name: "Remove Git source" }),
    );
    await userEvent.click(
      within(
        await body.findByRole("alertdialog", {
          name: "Remove the Git source?",
        }),
      ).getByRole("button", { name: "Remove source" }),
    );
    await waitFor(() =>
      expect(requests).toEqual([{ method: "DELETE", body: null }]),
    );
    await expect(await canvas.findByRole("status")).toHaveTextContent(
      /Git source removed/,
    );
  },
};
