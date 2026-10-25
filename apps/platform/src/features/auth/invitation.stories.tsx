import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, within } from "storybook/test";
import {
  accept,
  guest,
  member,
  reject,
  scenario,
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
  parameters: scenario(guest, accept("invitations/accept")),
} satisfies Meta<typeof Invitation>;
export default meta;
type Story = StoryObj<typeof meta>;

export const NewAccount: Story = {};
export const MissingLink: Story = { args: { token: undefined } };
export const ExistingAccount: Story = {
  parameters: scenario(member, accept("invitations/accept")),
};
export const Rejected: Story = {
  parameters: scenario(guest, reject("invitations/accept")),
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
    await submit(canvasElement);
    await expect(
      await within(canvasElement).findByRole("status"),
    ).toBeVisible();
  },
};
