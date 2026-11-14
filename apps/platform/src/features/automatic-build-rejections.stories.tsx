import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, within } from "storybook/test";
import type { Schema } from "../api/client";
import { AutomaticBuildRejections } from "./automatic-build-rejections";

const api = "00000000-0000-4000-8000-000000000501";
const worker = "00000000-0000-4000-8000-000000000502";
const web = "00000000-0000-4000-8000-000000000503";
const builder = "00000000-0000-4000-8000-000000000510";
const enrolling = "00000000-0000-4000-8000-000000000511";
const gone = "00000000-0000-4000-8000-000000000512";

const applications = [
  { id: api, name: "api" },
  { id: worker, name: "worker" },
  { id: web, name: "web" },
] as Schema["Specification"]["applications"];
const servers = [
  { id: builder, name: "builder-1" },
  { id: enrolling, name: "builder-2" },
] as Schema["Server"][];

const meta = {
  title: "Pipelines/Automatic build notices",
  component: AutomaticBuildRejections,
  args: { applications, servers, error: null },
} satisfies Meta<typeof AutomaticBuildRejections>;
export default meta;
type Story = StoryObj<typeof meta>;

// Between upgrading management and its runners, an automatic build waits for its runner and says
// which runner it waits for, rather than being refused or holding the refresh silently.
export const WaitingForTheirRunner: Story = {
  args: {
    rejections: [
      {
        applicationId: web,
        commit: "c".repeat(40),
        reason: "runner_unavailable",
        rejectedAt: "2026-11-14T09:00:00Z",
      },
    ],
    waits: [
      {
        applicationId: api,
        commit: "a".repeat(40),
        reason: "runner_update",
        runnerId: builder,
        since: "2026-11-14T10:00:00Z",
      },
      {
        applicationId: worker,
        commit: "b".repeat(40),
        reason: "runner_enrollment",
        runnerId: enrolling,
        since: "2026-11-14T10:00:00Z",
      },
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const waiting = canvas.getByRole("status", {
      name: "Automatic builds waiting",
    });
    await expect(waiting).toHaveTextContent(
      `api · ${"a".repeat(12)} · Waiting for runner builder-1 to update to the release management runs.`,
    );
    await expect(waiting).toHaveTextContent(
      `worker · ${"b".repeat(12)} · Waiting for runner builder-2 to finish enrolling.`,
    );
    await expect(waiting).toHaveTextContent("within a day");
    // A build that waited its day is rejected, and the rejection says so.
    await expect(
      canvas.getByRole("status", { name: "Automatic build rejections" }),
    ).toHaveTextContent(
      "waited a day for it to be updated or to finish enrolling",
    );
  },
};

// A runner the dashboard can't name, as one no longer listed, is still identified.
export const WaitingForARunnerNotListed: Story = {
  args: {
    rejections: [],
    waits: [
      {
        applicationId: api,
        commit: "a".repeat(40),
        reason: "runner_update",
        runnerId: gone,
        since: "2026-11-14T10:00:00Z",
      },
    ],
  },
  play: async ({ canvasElement }) => {
    await expect(
      within(canvasElement).getByRole("status", {
        name: "Automatic builds waiting",
      }),
    ).toHaveTextContent(`Waiting for runner ${gone.slice(0, 8)} to update`);
  },
};
