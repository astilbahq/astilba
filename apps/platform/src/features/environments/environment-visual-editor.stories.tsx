import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, within } from "storybook/test";
import type { Schema } from "../../api/client";
import {
  ShellPreview,
  session,
  workspace,
  examples,
  environments,
  projectHandlers,
} from "../../workshop/app-preview";
import {
  draftPath,
  postgresReleases,
  revision,
} from "../../workshop/environment-fixtures";
import { scenario } from "../../workshop/fixtures";
import { configure } from "./environment-detail.scenarios";

const path = `/?workspace=${workspace.id}&project=${examples[0].id}&environment=${environments[0].id}`;
const meta = {
  title: "Pages/Environment detail",
  component: ShellPreview,
  args: { initialPath: path },
  parameters: scenario(session, ...projectHandlers()),
} satisfies Meta<typeof ShellPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

const canvasVolume = {
  id: "00000000-0000-4000-8000-000000000400",
  name: "api-data",
};
const canvasRevision: Schema["Revision"] = {
  ...revision,
  specification: {
    ...revision.specification,
    applications: revision.specification.applications.map((app, index) =>
      index === 0
        ? {
            ...app,
            mounts: [
              { volumeId: canvasVolume.id, target: "/data", readOnly: false },
            ],
          }
        : app,
    ),
    volumes: [canvasVolume],
    bindings: [
      {
        id: "00000000-0000-4000-8000-000000000500",
        sourceId: revision.specification.applications[0].id,
        targetId: revision.specification.applications[1].id,
        output: "HOST",
        variable: "API_HOST",
      },
    ],
  },
};
const canvasParameters = scenario(
  session,
  http.get(draftPath, () => HttpResponse.json(canvasRevision)),
  ...projectHandlers(),
);
const emptyParameters = (readOnly = false) =>
  scenario(
    readOnly
      ? { ...session, workspaces: [{ ...workspace, role: "viewer" }] }
      : session,
    http.get(draftPath, () =>
      HttpResponse.json({
        ...revision,
        specification: { version: 1, applications: [], variables: [] },
      }),
    ),
    ...projectHandlers(),
  );

export const Canvas: Story = {
  parameters: canvasParameters,
  play: async ({ canvasElement, loaded }) => {
    await configure(canvasElement, loaded.workshop.instance);
  },
};
export const CanvasInspector: Story = {
  parameters: canvasParameters,
  play: async ({ canvasElement, loaded }) => {
    const canvas = await configure(canvasElement, loaded.workshop.instance);
    await userEvent.click(canvas.getByRole("button", { name: "Service list" }));
    await userEvent.click(canvas.getByRole("button", { name: "api" }));
    await userEvent.click(canvas.getByRole("button", { name: "Canvas" }));
    await expect(
      within(
        canvas.getByRole("region", { name: "Properties for api" }),
      ).getByRole("textbox", { name: "Service name" }),
    ).toHaveValue("api");
  },
};
export const CanvasVolume: Story = {
  parameters: canvasParameters,
  play: async ({ canvasElement, loaded }) => {
    const canvas = await configure(canvasElement, loaded.workshop.instance);
    await userEvent.click(canvas.getByRole("button", { name: "Service list" }));
    await userEvent.click(canvas.getByRole("button", { name: "api-data" }));
    await userEvent.click(canvas.getByRole("button", { name: "Canvas" }));
    await expect(
      within(
        canvas.getByRole("region", { name: "Properties for api-data" }),
      ).getByRole("textbox", { name: "Volume name" }),
    ).toHaveValue("api-data");
  },
};
export const EmptyCanvas: Story = {
  parameters: emptyParameters(),
  play: async ({ canvasElement, loaded }) => {
    await configure(canvasElement, loaded.workshop.instance);
  },
};
export const ReadOnlyCanvas: Story = {
  parameters: scenario(
    { ...session, workspaces: [{ ...workspace, role: "viewer" }] },
    http.get(draftPath, () => HttpResponse.json(canvasRevision)),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement, loaded }) => {
    const canvas = await configure(canvasElement, loaded.workshop.instance);
    await userEvent.click(canvas.getByRole("button", { name: "Service list" }));
    await userEvent.click(canvas.getByRole("button", { name: "api" }));
    await userEvent.click(canvas.getByRole("button", { name: "Canvas" }));
    await expect(
      canvas.getByText("Configuration is read only.", { exact: false }),
    ).toBeVisible();
  },
};
export const CanvasLongNames: Story = {
  parameters: scenario(
    session,
    http.get(draftPath, () =>
      HttpResponse.json({
        ...canvasRevision,
        specification: {
          ...canvasRevision.specification,
          applications: canvasRevision.specification.applications.map(
            (app, index) =>
              index === 0
                ? {
                    ...app,
                    name: "customer-facing-international-application-service",
                    source: {
                      kind: "image",
                      image:
                        "registry.example.com/team/customer-facing-international-application:release",
                    },
                  }
                : app,
          ),
        },
      } satisfies Schema["Revision"]),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement, loaded }) => {
    const canvas = await configure(canvasElement, loaded.workshop.instance);
    await userEvent.click(canvas.getByRole("button", { name: "Service list" }));
    await userEvent.click(
      canvas.getByRole("button", {
        name: "customer-facing-international-application-service",
      }),
    );
    await userEvent.click(canvas.getByRole("button", { name: "Canvas" }));
    await expect(
      within(
        canvas.getByRole("region", {
          name: "Properties for customer-facing-international-application-service",
        }),
      ).getByRole("textbox", { name: "Service name" }),
    ).toHaveValue("customer-facing-international-application-service");
  },
};
export const EmptyReadOnlyCanvas: Story = {
  parameters: emptyParameters(true),
  play: async ({ canvasElement, loaded }) => {
    const canvas = await configure(canvasElement, loaded.workshop.instance);
    await userEvent.click(canvas.getByRole("button", { name: "Canvas" }));
  },
};

// A database still on a retired release offers the in-place update to its major's current one.
const retired = postgresReleases.find((r) => r.major === 18 && !r.current)!;
const current = postgresReleases.find((r) => r.major === 18 && r.current)!;
const databaseVolume = {
  id: "00000000-0000-4000-8000-000000000401",
  name: "database-data",
};
export const CanvasRetiredPostgres: Story = {
  parameters: scenario(
    session,
    http.get(draftPath, () =>
      HttpResponse.json({
        ...revision,
        specification: {
          ...revision.specification,
          applications: [
            {
              id: "00000000-0000-4000-8000-000000000302",
              name: "database",
              kind: "postgres",
              source: { kind: "image", image: retired.image },
              postgres: {
                database: "app",
                username: "app",
                passwordSecretVersionId: "00000000-0000-4000-8000-000000000600",
              },
              mounts: [
                {
                  volumeId: databaseVolume.id,
                  target: retired.mount,
                  readOnly: false,
                },
              ],
            },
          ],
          volumes: [databaseVolume],
        },
      } satisfies Schema["Revision"]),
    ),
    ...projectHandlers(),
  ),
  play: async ({ canvasElement, loaded }) => {
    const canvas = await configure(canvasElement, loaded.workshop.instance);
    await userEvent.click(canvas.getByRole("button", { name: "Service list" }));
    await userEvent.click(canvas.getByRole("button", { name: "database" }));
    await userEvent.click(canvas.getByRole("button", { name: "Canvas" }));
    const properties = within(
      canvas.getByRole("region", { name: "Properties for database" }),
    );
    await expect(
      await properties.findByText(`PostgreSQL ${retired.name} is retired.`, {
        exact: false,
      }),
    ).toBeVisible();
    await userEvent.click(
      properties.getByRole("button", {
        name: `Stage update to PostgreSQL ${current.name}`,
      }),
    );
    await expect(
      await properties.findByText(`PostgreSQL ${current.name}`, {
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      properties.queryByRole("button", { name: /Stage update/ }),
    ).toBeNull();
  },
};
