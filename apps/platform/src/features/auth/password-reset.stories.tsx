import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, waitFor, within } from "storybook/test";
import {
  accept,
  guest,
  reject,
  scenario,
  submitting,
} from "../../workshop/fixtures";
import { fill, submit } from "../../workshop/interactions";
import { AuthFrame } from "./auth-frame";
import { PasswordReset } from "./password-reset";

const meta = {
  title: "Pages/Password reset",
  component: PasswordReset,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  parameters: scenario(guest, accept("reset/request")),
} satisfies Meta<typeof PasswordReset>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Request: Story = {};
export const EmailSent: Story = {
  play: async ({ canvasElement }) => {
    await fill(canvasElement, "email", "alex@example.test");
    await submit(canvasElement);
    await expect(
      await within(canvasElement).findByRole("status"),
    ).toBeVisible();
  },
};
export const NewPassword: Story = {
  args: { token: "workshop-reset-token" },
  parameters: scenario(
    guest,
    accept("reset/validate"),
    accept("reset/complete"),
  ),
};
export const InvalidLink: Story = {
  ...NewPassword,
  parameters: scenario(guest, reject("reset/validate")),
  play: async ({ canvasElement }) => {
    await waitFor(() =>
      expect(
        within(canvasElement).getByRole("heading", { level: 1 }),
      ).toHaveFocus(),
    );
    await expect(canvasElement.querySelector("form")).not.toBeInTheDocument();
  },
};
export const ExpiredDuringEntry: Story = {
  ...NewPassword,
  parameters: scenario(
    guest,
    accept("reset/validate"),
    reject("reset/complete"),
  ),
  play: async ({ canvasElement }) => {
    await fill(canvasElement, "password", "correct horse battery staple");
    await submit(canvasElement);
    await expect(await within(canvasElement).findByRole("alert")).toBeVisible();
  },
};
export const CheckingLink: Story = {
  ...NewPassword,
  parameters: scenario(guest, submitting("reset/validate")),
};
export const CheckUnavailable: Story = {
  ...NewPassword,
  parameters: scenario(
    guest,
    http.post("/api/v1/auth/reset/validate", () => HttpResponse.error()),
  ),
  play: async ({ canvasElement }) => {
    await expect(
      await within(within(canvasElement).getByRole("main")).findByRole(
        "button",
      ),
    ).toBeVisible();
  },
};
export const Complete: Story = {
  ...NewPassword,
  play: async ({ canvasElement }) => {
    await fill(canvasElement, "password", "correct horse battery staple");
    await submit(canvasElement);
    await expect(
      await within(canvasElement).findByRole("status"),
    ).toBeVisible();
  },
};
export const Sending: Story = {
  parameters: scenario(guest, submitting("reset/request")),
  play: async ({ canvasElement }) => {
    await fill(canvasElement, "email", "alex@example.test");
    await submit(canvasElement);
    await expect(
      canvasElement.querySelector('button[type="submit"]'),
    ).toBeDisabled();
  },
};
