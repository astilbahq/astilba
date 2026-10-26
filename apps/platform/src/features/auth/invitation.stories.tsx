import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, waitFor, within } from "storybook/test";
import {
  accept,
  guest,
  member,
  reject,
  scenario,
  submitting,
} from "../../workshop/fixtures";
import { fill, submit } from "../../workshop/interactions";
import { AuthFrame } from "./auth-frame";
import { Invitation } from "./invitation";

const meta = {
  title: "Pages/Invitation",
  component: Invitation,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  args: { token: "workshop-invitation-token" },
  parameters: scenario(
    guest,
    accept("invitations/validate"),
    accept("invitations/accept"),
  ),
} satisfies Meta<typeof Invitation>;
export default meta;
type Story = StoryObj<typeof meta>;

export const NewAccount: Story = {};
export const MissingLink: Story = { args: { token: undefined } };
export const ExistingAccount: Story = {
  parameters: scenario(
    member,
    accept("invitations/validate"),
    accept("invitations/accept"),
  ),
};
export const Rejected: Story = {
  parameters: scenario(guest, reject("invitations/validate")),
  play: async ({ canvasElement }) => {
    await waitFor(() =>
      expect(
        within(canvasElement).getByRole("heading", { level: 1 }),
      ).toHaveFocus(),
    );
    await expect(canvasElement.querySelector("form")).not.toBeInTheDocument();
  },
};
export const CheckingLink: Story = {
  parameters: scenario(guest, submitting("invitations/validate")),
};
export const CheckUnavailable: Story = {
  parameters: scenario(
    guest,
    http.post("/api/v1/auth/invitations/validate", () => HttpResponse.error()),
  ),
  play: async ({ canvasElement }) => {
    await expect(
      await within(within(canvasElement).getByRole("main")).findByRole(
        "button",
      ),
    ).toBeVisible();
  },
};
export const ExpiredDuringEntry: Story = {
  parameters: scenario(
    guest,
    accept("invitations/validate"),
    reject("invitations/accept"),
  ),
  play: async ({ canvasElement }) => {
    await fill(canvasElement, "name", "Alex");
    await fill(canvasElement, "password", "correct horse battery staple");
    await submit(canvasElement);
    await expect(await within(canvasElement).findByRole("alert")).toBeVisible();
  },
};
export const Accepted: Story = {
  ...ExistingAccount,
  play: async ({ canvasElement }) => {
    await within(within(canvasElement).getByRole("main")).findByRole("button");
    await submit(canvasElement);
    await expect(
      await within(canvasElement).findByRole("status"),
    ).toBeVisible();
  },
};
