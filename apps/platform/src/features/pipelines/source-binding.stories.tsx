import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { useRef, useState } from "react";
import { flushSync } from "react-dom";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema, Session } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { useAction } from "../../ui";
import { member, scenario } from "../../workshop/fixtures";
import { ChangeSource, ImportSource, SourceFailure } from "./source-binding";

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
const spareId = "00000000-0000-4000-8000-000000000009";
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
      'Commit eeeeeeeeeeee doesn\'t have a file Astilba can import at "compose.yaml", which the source selects. It may be missing, a directory, a symlink or too large. If it was renamed or removed, bind the source again with the files the branch holds now.',
    failedAt: "2026-11-13T10:00:00Z",
  },
};
// The owner has confirmed their identity recently, so neither change asks again.
const owner: Session = { ...member, recentAuth: true };

// What the workshop server was asked to do, and the status it refuses each change with, if any,
// reset before each story.
let requests: { method: string; body: unknown }[] = [];
let refusePut: number | null = null;
let refuseDelete: number | null = null;
// The source the workshop server holds after a binding, whose hash its response names, reset before
// each story.
let served: Schema["GitSourceStatus"] = source;
const rebound: Schema["GitSourceStatus"] = {
  ...source,
  sourceHash: "1".repeat(64),
  failure: null,
  configuration: {
    ...source.configuration,
    entrypoints: ["docker-compose.yaml"],
    paths: ["docker-compose.yaml", "config/app.env"],
  },
};
const refusal = (status: number) =>
  HttpResponse.json(
    {
      code: status === 409 ? "conflict" : "not_found",
      message:
        status === 409
          ? "The source changed since it was shown, for example because another owner bound it again or removed it. Look at it as it is now, then bind it again."
          : "The requested resource was not found.",
      ...(status === 409 && { fields: ["sourceHash"] }),
      requestId: "00000000-0000-4000-8000-00000000000a",
    } satisfies Schema["Error"],
    { status },
  );

// Panel behaves as the page does: after a change it shows the source as the server now holds it,
// which next is, and moves focus to what it says. elsewhere is a change another owner makes, which
// the page's polling would show.
function Panel({
  shown,
  next,
  elsewhere,
}: {
  shown: Schema["GitSourceStatus"];
  next?: Schema["GitSourceStatus"];
  elsewhere?: Schema["GitSourceStatus"];
}) {
  const [said, setSaid] = useState("");
  const [current, setCurrent] = useState(shown);
  const status = useRef<HTMLParagraphElement>(null);
  return (
    <main id="main">
      <SourceFailure failure={current.failure} />
      <ChangeSource
        path={path}
        revisionId={revisionId}
        source={current}
        dirty={false}
        changed={async (message) => {
          flushSync(() => setSaid(message));
          status.current?.focus();
          // The page's refreshed source reaches the panel a moment later, as the query's
          // notification is scheduled after the refresh resolves.
          if (next) setTimeout(() => setCurrent(next), 0);
        }}
      />
      {said && (
        <p ref={status} role="status" tabIndex={-1}>
          {said}
        </p>
      )}
      {elsewhere && (
        <button type="button" onClick={() => setCurrent(elsewhere)}>
          Another owner changes the source
        </button>
      )}
    </main>
  );
}

// Importing shares its runner with the page's other Git forms, as the build form does here.
function Importing() {
  const action = useAction();
  const connection = useState("");
  const runner = useState("");
  return (
    <main id="main">
      <label>
        Build form runner
        <select value={runner[0]} onChange={(e) => runner[1](e.target.value)}>
          <option value="">None</option>
          <option value={runnerId}>builder</option>
          <option value={spareId}>spare</option>
        </select>
      </label>
      <ImportSource
        path={path}
        revisionId={revisionId}
        dirty={false}
        action={action}
        connection={connection}
        runner={runner}
        imported={async () => {}}
      />
    </main>
  );
}

const meta = {
  title: "Patterns/Git source binding",
  component: Panel,
  args: { shown: source },
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
        {
          id: spareId,
          name: "spare",
          role: "build",
          status: "online",
          capabilities: { build: true, arch: "arm64" },
        },
      ] as Schema["Server"][]),
    ),
    http.put(`${root}/source`, async ({ request }) => {
      requests.push({ method: "PUT", body: await request.json() });
      if (refusePut) return refusal(refusePut);
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
        { status: 202, headers: { "Astilba-Source-Hash": served.sourceHash } },
      );
    }),
    http.delete(`${root}/source`, () => {
      requests.push({ method: "DELETE", body: null });
      if (refuseDelete) return refusal(refuseDelete);
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  beforeEach: () => {
    requests = [];
    refusePut = null;
    refuseDelete = null;
    served = source;
  },
} satisfies Meta<typeof Panel>;
export default meta;
type Story = StoryObj<typeof meta>;

// A selected file renamed upstream stops automatic imports and says which file. The owner binds
// the source again with the new name, keeping everything else as it was.
export const RebindRenamedFile: Story = {
  args: { next: rebound },
  beforeEach: () => {
    served = rebound;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText(/import at "compose.yaml"/)).toBeVisible();
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
    // The owner's own binding is never shown as a change made elsewhere, not even for a moment.
    let noticed = false;
    const watch = new MutationObserver(() => {
      noticed ||= /The source changed since this form was opened/.test(
        canvasElement.textContent ?? "",
      );
    });
    watch.observe(canvasElement, {
      subtree: true,
      childList: true,
      characterData: true,
    });
    await userEvent.click(
      canvas.getByRole("button", { name: "Bind again and import" }),
    );
    await waitFor(() =>
      expect(requests).toEqual([
        {
          method: "PUT",
          body: {
            baseRevisionId: revisionId,
            sourceHash: "f".repeat(64),
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
    // The page then shows the source as bound, which isn't someone else's change.
    await expect(await canvas.findByText(/Source changed/)).toHaveFocus();
    // The page's refreshed source arrives after the binding settles, and is the binding's own.
    await new Promise((resolve) => setTimeout(resolve, 50));
    watch.disconnect();
    await expect(noticed).toBe(false);
    await expect(
      canvas.getByRole("textbox", {
        name: "Compose entrypoints, one path per line",
      }),
    ).toHaveValue("docker-compose.yaml");
  },
};

// Another owner's change that reaches the page while this owner's binding settles is still shown.
export const ChangedDuringRebind: Story = {
  args: {
    next: {
      ...rebound,
      sourceHash: "3".repeat(64),
      configuration: { ...rebound.configuration, autoDeploy: true },
    },
  },
  beforeEach: () => {
    served = rebound;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByText("Change or remove the Git source"));
    await waitFor(() =>
      expect(canvas.getByRole("option", { name: /builder/ })).toBeTruthy(),
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Bind again and import" }),
    );
    await expect(
      await canvas.findByText(/The source changed since this form was opened/),
    ).toBeVisible();
  },
};

// Binding again without changing anything leaves the source as it was; a change another owner makes
// afterwards is still shown, not taken over the owner's typing.
export const ChangedElsewhereAfterBinding: Story = {
  args: {
    elsewhere: {
      ...source,
      sourceHash: "2".repeat(64),
      configuration: { ...source.configuration, autoDeploy: true },
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByText("Change or remove the Git source"));
    await waitFor(() =>
      expect(canvas.getByRole("option", { name: /builder/ })).toBeTruthy(),
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Bind again and import" }),
    );
    await canvas.findByText(/Source changed/);
    const branch = canvas.getByRole("textbox", { name: "Branch" });
    await userEvent.clear(branch);
    await userEvent.type(branch, "release");
    await userEvent.click(
      canvas.getByRole("button", { name: "Another owner changes the source" }),
    );
    await expect(
      await canvas.findByText(/The source changed since this form was opened/),
    ).toBeVisible();
    await expect(branch).toHaveValue("release");
  },
};

// The import's fallback can't be the runner, however the runner was chosen, so what is sent is what
// is shown.
export const ImportSharesItsRunner: Story = {
  render: () => <Importing />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByText("Import Compose from Git"));
    await waitFor(() =>
      expect(
        canvas.getByRole("option", { name: /App · team\/app/ }),
      ).toBeTruthy(),
    );
    await userEvent.selectOptions(
      canvas.getByRole("combobox", { name: "Repository connection" }),
      connectionId,
    );
    await userEvent.type(
      canvas.getByRole("textbox", { name: "Branch" }),
      "main",
    );
    await userEvent.selectOptions(
      canvas.getByRole("combobox", { name: "Build runner" }),
      runnerId,
    );
    const fallback = canvas.getByRole("combobox", { name: "Fallback runner" });
    await userEvent.selectOptions(fallback, spareId);
    await userEvent.selectOptions(
      canvas.getByRole("combobox", { name: "Build form runner" }),
      spareId,
    );
    await expect(fallback).toHaveDisplayValue("Queue on selected runner");
    await userEvent.click(
      canvas.getByRole("button", { name: "Import source" }),
    );
    await waitFor(() => expect(requests).toHaveLength(1));
    const sent = requests[0]!.body as Schema["BindGitSource"];
    await expect(sent.configuration.runnerId).toBe(spareId);
    await expect(sent.configuration.fallbackRunnerId).toBeUndefined();
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
    // Once the dialog is gone it hasn't taken focus back from what the page says.
    await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull());
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await expect(canvas.getByText(/Git source removed/)).toHaveFocus();
  },
};

// Another owner changed the source while this form was open, so binding from it is refused. The
// owner's typing is kept, the form says why, and starting again shows the source as it is now and
// binds that.
export const ChangedSinceShown: Story = {
  args: {
    next: {
      ...source,
      sourceHash: "e".repeat(64),
      configuration: { ...source.configuration, autoDeploy: true },
    },
  },
  beforeEach: () => {
    refusePut = 409;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByText("Change or remove the Git source"));
    const branch = canvas.getByRole("textbox", { name: "Branch" });
    await userEvent.clear(branch);
    await userEvent.type(branch, "release");
    await userEvent.click(
      canvas.getByRole("button", { name: "Bind again and import" }),
    );
    await expect(await canvas.findByText(/was not bound again/)).toHaveFocus();
    await expect(
      canvas.getByText(/The source changed since this form was opened/),
    ).toBeVisible();
    await expect(branch).toHaveValue("release");
    refusePut = null;
    await userEvent.click(
      canvas.getByRole("button", {
        name: "Start again from the current source",
      }),
    );
    await expect(
      canvas.getByText("Change or remove the Git source"),
    ).toHaveFocus();
    await expect(canvas.getByRole("textbox", { name: "Branch" })).toHaveValue(
      "main",
    );
    await expect(
      canvas.getByRole("checkbox", { name: /Automatically deploy/ }),
    ).toBeChecked();
    await userEvent.click(
      canvas.getByRole("button", { name: "Bind again and import" }),
    );
    await waitFor(() => expect(requests).toHaveLength(2));
    await expect(
      (requests[1]!.body as Schema["BindGitSource"]).sourceHash,
    ).toBe("e".repeat(64));
  },
};

// A fallback that is no longer one of the workspace's runners is shown as such, and promoting the
// fallback to the runner leaves none, so what is sent is what is shown.
export const FallbackRunnerChanges: Story = {
  args: {
    shown: {
      ...source,
      configuration: {
        ...source.configuration,
        fallbackRunnerId: "00000000-0000-4000-8000-0000000000ff",
      },
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByText("Change or remove the Git source"));
    const fallback = canvas.getByRole("combobox", { name: "Fallback runner" });
    await waitFor(() =>
      expect(fallback).toHaveDisplayValue(
        "A runner no longer in this workspace",
      ),
    );
    await userEvent.selectOptions(fallback, spareId);
    await userEvent.selectOptions(
      canvas.getByRole("combobox", { name: "Build runner" }),
      spareId,
    );
    await expect(fallback).toHaveDisplayValue("Queue on selected runner");
    await userEvent.click(
      canvas.getByRole("button", { name: "Bind again and import" }),
    );
    await waitFor(() => expect(requests).toHaveLength(1));
    const sent = requests[0]!.body as Schema["BindGitSource"];
    await expect(sent.configuration.runnerId).toBe(spareId);
    await expect(sent.configuration.fallbackRunnerId).toBeUndefined();
  },
};

// A refused removal stays in its dialog, beside the control that asked, with focus kept there.
export const RemoveRefused: Story = {
  beforeEach: () => {
    refuseDelete = 404;
  },
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
    const confirm = within(dialog).getByRole("button", {
      name: "Remove source",
    });
    await userEvent.click(confirm);
    await expect(
      await within(dialog).findByText("The requested resource was not found."),
    ).toBeVisible();
    await expect(confirm).toHaveFocus();
    await expect(canvas.queryByRole("status")).toBeNull();
  },
};
