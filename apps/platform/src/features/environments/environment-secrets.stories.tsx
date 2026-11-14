import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../../api/client";
import {
  ShellPreview,
  session,
  workspace,
  examples,
  environments,
  projectHandlers,
} from "../../workshop/app-preview";
import { environmentRoot } from "../../workshop/environment-fixtures";
import { scenario } from "../../workshop/fixtures";
import {
  listing,
  members,
  retired,
  volumes,
} from "../../workshop/retirement-fixtures";

const path = `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${environments[0].id}`;
const meta = {
  title: "Pages/Environment secrets",
  component: ShellPreview,
  args: { initialPath: path },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

const token: Schema["SecretVersion"] = {
  id: "00000000-0000-4000-8000-000000000110",
  secretId: "00000000-0000-4000-8000-000000000111",
  name: "API_TOKEN",
  createdAt: "2026-11-01T12:00:00Z",
};
const database: Schema["SecretVersion"] = {
  id: "00000000-0000-4000-8000-000000000112",
  secretId: "00000000-0000-4000-8000-000000000113",
  name: "DATABASE_URL",
  createdAt: "2026-11-02T12:00:00Z",
};
// API_TOKEN's earlier versions, newest first, as the list returns them after its current one.
const replaced: Schema["SecretVersion"] = {
  ...token,
  id: "00000000-0000-4000-8000-000000000114",
  createdAt: "2026-10-20T12:00:00Z",
};
const original: Schema["SecretVersion"] = {
  ...token,
  id: "00000000-0000-4000-8000-000000000115",
  createdAt: "2026-10-01T12:00:00Z",
};
const secrets = http.get(`${environmentRoot}/secrets`, () =>
  HttpResponse.json([token, replaced, original, database]),
);
const inUse = http.delete(`${environmentRoot}/secrets/:secretId`, () =>
  HttpResponse.json(
    {
      code: "conflict",
      message: "Server text the dashboard does not show.",
      requestId: "workshop",
      fields: ["secret.inUse"],
    } satisfies Schema["Error"],
    { status: 409 },
  ),
);

async function openSecrets(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.click(await canvas.findByRole("tab", { name: "Settings" }));
  return canvas.findByRole("heading", { name: "Secrets" });
}

async function openRetireDialog(canvasElement: HTMLElement) {
  await openSecrets(canvasElement);
  await userEvent.click(
    await within(canvasElement).findByRole("button", {
      name: "Retire secret API_TOKEN",
    }),
  );
  return within(canvasElement.ownerDocument.body).findByRole("alertdialog", {
    name: "Retire API_TOKEN?",
  });
}

// Retiring erases every version and takes the secret out of the list, which is announced.
let retiredSecrets: string[] = [];
export const RetireSecret: Story = {
  beforeEach: () => {
    retiredSecrets = [];
  },
  parameters: scenario(
    session,
    http.get(`${environmentRoot}/secrets`, () =>
      HttpResponse.json(
        [token, database].filter(
          (secret) => !retiredSecrets.includes(secret.secretId),
        ),
      ),
    ),
    http.delete(`${environmentRoot}/secrets/:secretId`, ({ params }) => {
      retiredSecrets.push(String(params.secretId));
      return new HttpResponse(null, { status: 204 });
    }),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const heading = await openSecrets(canvasElement);
    const regions = canvas.getAllByRole("status");
    const dialog = await openRetireDialog(canvasElement);
    await expect(dialog).toHaveAccessibleDescription(
      /cannot be undone\..*The value of every version is erased\./,
    );
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Retire secret" }),
    );
    const message = await canvas.findByText(
      "Secret API_TOKEN retired. The values of all its versions are erased.",
    );
    await expect(regions).toContain(message.closest('[role="status"]'));
    await expect(retiredSecrets).toEqual([token.secretId]);
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    await expect(canvas.queryByText("API_TOKEN")).toBeNull();
    await expect(canvas.getByText("DATABASE_URL")).toBeVisible();
    await waitFor(() => expect(heading).toHaveFocus());
  },
};
// A secret something still uses is refused with the catalogue's explanation, and stays listed.
export const RetireSecretInUse: Story = {
  parameters: scenario(session, secrets, inUse, ...projectHandlers()),
  play: async ({ canvasElement }) => {
    const dialog = within(await openRetireDialog(canvasElement));
    await userEvent.click(
      dialog.getByRole("button", { name: "Retire secret" }),
    );
    await expect(await dialog.findByRole("alert")).toHaveTextContent(
      "This secret is still in use. The draft, the running deployment, a deployment plan, an operation or a database upgrade uses one of its versions. Replace it there, then try again.",
    );
    await expect(
      dialog.getByRole("button", { name: "Retire secret" }),
    ).toBeEnabled();
  },
};
async function openRetireVersionDialog(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await openSecrets(canvasElement);
  await userEvent.click(await canvas.findByText("2 earlier versions"));
  await userEvent.click(
    canvas.getByRole("button", {
      name: `Retire version ${replaced.id} of API_TOKEN`,
    }),
  );
  return within(canvasElement.ownerDocument.body).findByRole("alertdialog", {
    name: "Retire this version of API_TOKEN?",
  });
}

// Retiring an earlier version erases only its value: the secret, its current version and its
// other versions stay, and the change is announced.
let retiredVersions: string[] = [];
export const RetireSecretVersion: Story = {
  beforeEach: () => {
    retiredVersions = [];
  },
  parameters: scenario(
    session,
    http.get(`${environmentRoot}/secrets`, () =>
      HttpResponse.json(
        [token, replaced, original, database].filter(
          (version) => !retiredVersions.includes(version.id),
        ),
      ),
    ),
    http.delete(
      `${environmentRoot}/secrets/:secretId/versions/:versionId`,
      ({ params }) => {
        retiredVersions.push(String(params.versionId));
        return new HttpResponse(null, { status: 204 });
      },
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await openSecrets(canvasElement);
    await expect(
      canvas.queryByRole("button", {
        name: /^Retire version .* of DATABASE_URL$/,
      }),
    ).toBeNull();
    const regions = canvas.getAllByRole("status");
    const dialog = await openRetireVersionDialog(canvasElement);
    await expect(dialog).toHaveAccessibleDescription(
      /cannot be undone\..*Its value is erased.*keeps its current version/,
    );
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Retire version" }),
    );
    const message = await canvas.findByText(
      `Version ${replaced.id} of API_TOKEN retired. Its value is erased.`,
    );
    await expect(regions).toContain(message.closest('[role="status"]'));
    await expect(retiredVersions).toEqual([replaced.id]);
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    await expect(canvas.queryByText(replaced.id)).toBeNull();
    await expect(canvas.getByText(token.id)).toBeVisible();
    await expect(canvas.getByText("1 earlier version")).toBeVisible();
  },
};
// A version something still uses, or one that became current, is refused with the catalogue's
// explanation, and stays listed.
export const RetireSecretVersionRefused: Story = {
  parameters: scenario(
    session,
    secrets,
    http.delete(
      `${environmentRoot}/secrets/:secretId/versions/:versionId`,
      () =>
        HttpResponse.json(
          {
            code: "conflict",
            message: "Server text the dashboard does not show.",
            requestId: "workshop",
            fields: ["secretVersion.inUse"],
          } satisfies Schema["Error"],
          { status: 409 },
        ),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const dialog = within(await openRetireVersionDialog(canvasElement));
    await userEvent.click(
      dialog.getByRole("button", { name: "Retire version" }),
    );
    await expect(await dialog.findByRole("alert")).toHaveTextContent(
      "This version is still in use. The draft, the running deployment, a deployment plan, an operation or a database upgrade uses it. Replace it there, then try again.",
    );
    await expect(
      dialog.getByRole("button", { name: "Retire version" }),
    ).toBeEnabled();
  },
};
// A session that is not recent confirms identity in place before the request.
export const RetireSecretIdentity: Story = {
  parameters: scenario(
    { ...session, recentAuth: false },
    secrets,
    inUse,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const dialog = await openRetireDialog(canvasElement);
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Retire secret" }),
    );
    const confirmation = await within(
      canvasElement.ownerDocument.body,
    ).findByRole("alertdialog", { name: "Confirm your identity" });
    await userEvent.click(
      within(confirmation).getByRole("button", { name: "Cancel" }),
    );
    await waitFor(() => expect(confirmation).not.toBeInTheDocument());
    await expect(dialog).toBeInTheDocument();
    await expect(within(dialog).queryByRole("alert")).toBeNull();
  },
};
// Operators save versions but cannot retire secrets.
export const SecretsOperator: Story = {
  parameters: scenario(
    { ...session, workspaces: [{ ...workspace, role: "operator" }] },
    secrets,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await openSecrets(canvasElement);
    await canvas.findByText("API_TOKEN");
    await expect(
      canvas.getByRole("button", { name: "Save secret version" }),
    ).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: /^Retire secret/ }),
    ).toBeNull();
    // Earlier versions are listed, without a way to retire them.
    await userEvent.click(canvas.getByText("2 earlier versions"));
    await expect(canvas.getByText(replaced.id)).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: /^Retire version/ }),
    ).toBeNull();
  },
};
// A retired environment is read-only, so its secrets are listed without actions.
export const SecretsRetiredEnvironment: Story = {
  parameters: scenario(
    session,
    listing(retired),
    members,
    volumes,
    secrets,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await openSecrets(canvasElement);
    await canvas.findByText("API_TOKEN");
    await expect(
      canvas.queryByRole("button", { name: /^Retire secret/ }),
    ).toBeNull();
    await expect(
      canvas.queryByRole("button", { name: "Save secret version" }),
    ).toBeNull();
  },
};
// A managed preview's owner cannot retire its secrets, which the server refuses.
export const SecretsManagedPreview: Story = {
  parameters: scenario(
    session,
    listing({ ...environments[0], purpose: "preview" }),
    secrets,
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await openSecrets(canvasElement);
    await canvas.findByText("API_TOKEN");
    await expect(
      canvas.queryByRole("button", { name: /^Retire secret/ }),
    ).toBeNull();
  },
};
