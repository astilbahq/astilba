import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { delay } from "msw";
import { http, HttpResponse } from "msw/http";
import { expect, waitFor, within } from "storybook/test";
import {
  guest,
  hostedMember as member,
  hostedScenario,
  hostedWorkspaceId,
  hostedSubscription,
  scenario,
} from "../../workshop/fixtures";
import { fill, submit } from "../../workshop/interactions";
import { AuthFrame } from "./auth-frame";
import { Signup } from "./signup";

const valid = http.post(
  "/api/v1/hosting/signup/validate",
  () => new HttpResponse(null, { status: 204 }),
);
const created = http.post("/api/v1/hosting/signup", () =>
  HttpResponse.json({ ...member, createdWorkspaceId: hostedWorkspaceId }),
);
const meta = {
  title: "Pages/Hosted signup",
  component: Signup,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  args: { token: "workshop-hosted-invitation-token" },
  parameters: hostedScenario(guest, valid, created),
} satisfies Meta<typeof Signup>;
export default meta;
type Story = StoryObj<typeof meta>;

export const NewAccount: Story = {};
export const ExistingAccount: Story = {
  parameters: hostedScenario(
    member,
    valid,
    created,
    http.get("/api/v1/workspaces/:workspaceId/subscription", () =>
      HttpResponse.json({
        ...hostedSubscription,
        managementActive: true,
        status: "granted",
      }),
    ),
  ),
};
export const MissingLink: Story = { args: { token: undefined } };
export const SelfHosted: Story = { parameters: scenario(guest) };
export const CheckingLink: Story = {
  parameters: hostedScenario(
    guest,
    http.post("/api/v1/hosting/signup/validate", async () => {
      await delay("infinite");
      return new HttpResponse(null, { status: 204 });
    }),
  ),
};
export const InvalidLink: Story = {
  parameters: hostedScenario(
    guest,
    http.post("/api/v1/hosting/signup/validate", () =>
      HttpResponse.json(
        {
          code: "invalid_credentials",
          message: "Workshop response.",
          requestId: "workshop-request",
        },
        { status: 400 },
      ),
    ),
  ),
};
export const CheckUnavailable: Story = {
  parameters: hostedScenario(
    guest,
    http.post("/api/v1/hosting/signup/validate", () => HttpResponse.error()),
  ),
};
export const ValidationErrors: Story = {
  play: async ({ canvasElement }) => {
    await within(canvasElement).findByRole("form");
    await submit(canvasElement);
    await expect(
      canvasElement.querySelector('input[name="workspace"]'),
    ).toHaveFocus();
  },
};
export const ExpiredDuringEntry: Story = {
  parameters: hostedScenario(
    guest,
    valid,
    http.post("/api/v1/hosting/signup", () =>
      HttpResponse.json(
        {
          code: "invalid_credentials",
          message: "Workshop response.",
          requestId: "workshop-request",
        },
        { status: 400 },
      ),
    ),
  ),
  play: async ({ canvasElement }) => {
    await fill(canvasElement, "workspace", "My workspace");
    await fill(canvasElement, "name", "Alex");
    await fill(canvasElement, "password", "correct horse battery staple");
    await submit(canvasElement);
    await expect(await within(canvasElement).findByRole("alert")).toBeVisible();
  },
};
export const Complete: Story = {
  ...ExistingAccount,
  play: async ({ canvasElement }) => {
    await fill(canvasElement, "workspace", "My workspace");
    await submit(canvasElement);
    await waitFor(async () => {
      await expect(within(canvasElement).getAllByRole("listitem")).toHaveLength(
        4,
      );
      await expect(
        canvasElement.querySelector('[aria-current="step"]'),
      ).not.toBeInTheDocument();
      await expect(within(canvasElement).getByRole("status")).toBeVisible();
    });
  },
};
