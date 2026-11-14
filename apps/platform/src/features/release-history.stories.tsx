import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, within } from "storybook/test";
import type { Schema } from "../api/client";
import {
  ShellPreview,
  session,
  workspace,
  examples,
  environments,
  projectHandlers,
} from "../workshop/app-preview";
import { environmentRoot, revision } from "../workshop/environment-fixtures";
import { scenario } from "../workshop/fixtures";

const path = `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${environments[0].id}`;
const meta = {
  title: "Pages/Release history",
  component: ShellPreview,
  args: { initialPath: path },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

// A release names secret versions by ID, so the history says, before any draft is made, which
// secrets a rollback would return to an earlier version, and which of those were retired.
export const RollbackReturnsOlderSecrets: Story = {
  parameters: scenario(
    session,
    http.get(`${environmentRoot}/releases`, () =>
      HttpResponse.json([
        {
          planId: "00000000-0000-4000-8000-000000000400",
          revisionId: revision.id,
          createdAt: revision.createdAt,
          applications: [
            {
              id: revision.specification.applications[0].id,
              name: "api",
              image: "nginx:alpine",
              eligible: true,
              note: "",
              changes: ["Variables and secret-version references"],
              olderSecretVersions: [
                {
                  secretId: "00000000-0000-4000-8000-000000000410",
                  versionId: "00000000-0000-4000-8000-000000000411",
                  name: "API_TOKEN",
                  retired: false,
                },
                {
                  secretId: "00000000-0000-4000-8000-000000000412",
                  versionId: "00000000-0000-4000-8000-000000000413",
                  name: "DATABASE_URL",
                  retired: true,
                },
              ],
            },
          ],
        },
      ] satisfies Schema["ReleaseHistoryEntry"][]),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("tab", { name: "Deployments" }),
    );
    await userEvent.click(await canvas.findByText(/^Revision 00000000/));
    await expect(
      canvas.getByText(
        /^Rolling back returns these secrets to an earlier version\./,
      ),
    ).toBeVisible();
    const token = canvas.getByText("API_TOKEN").closest("li");
    await expect(token).toHaveTextContent(
      "00000000-0000-4000-8000-000000000411",
    );
    await expect(token).not.toHaveTextContent("retired");
    await expect(
      canvas.getByText("DATABASE_URL").closest("li"),
    ).toHaveTextContent(/retired: its value is erased/);
  },
};
