import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { HttpResponse, http } from "msw/http";
import { expect, waitFor, within } from "storybook/test";
import { guest, reject, scenario, submitting } from "../../workshop/fixtures";
import { credentials, submit } from "../../workshop/interactions";
import { AuthFrame } from "./auth-frame";
import { SignIn } from "./sign-in";

const meta = {
  title: "Pages/Sign in",
  component: SignIn,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  parameters: scenario(guest, reject("login")),
} satisfies Meta<typeof SignIn>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const ValidationErrors: Story = {
  play: async ({ canvasElement }) => {
    await submit(canvasElement);
    await expect(
      canvasElement.querySelector('input[name="email"]'),
    ).toHaveAttribute("aria-invalid", "true");
  },
};
export const RejectedCredentials: Story = {
  play: async ({ canvasElement }) => {
    await credentials(canvasElement);
    await expect(await within(canvasElement).findByRole("alert")).toBeVisible();
  },
};
export const Submitting: Story = {
  parameters: scenario(guest, submitting("login")),
  play: async ({ canvasElement }) => {
    await credentials(canvasElement);
    await expect(
      canvasElement.querySelector('button[type="submit"]'),
    ).toBeDisabled();
  },
};
export const NetworkFailure: Story = {
  parameters: scenario(
    guest,
    http.post("/api/v1/auth/login", () => HttpResponse.error()),
  ),
  play: async ({ canvasElement }) => {
    await credentials(canvasElement);
    const notification = await within(
      canvasElement.ownerDocument.body,
    ).findByRole("dialog");
    await waitFor(() => expect(notification).toBeVisible());
    await expect(
      within(canvasElement).queryByRole("alert"),
    ).not.toBeInTheDocument();
  },
};
