import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import {
  pendingMFA,
  reject,
  scenario,
  submitting,
} from "../../workshop/fixtures";
import { authenticationCode, fill, submit } from "../../workshop/interactions";
import { AuthFrame } from "./auth-frame";
import { MFAChallenge } from "./mfa-challenge";

const meta = {
  title: "Pages/Two-factor sign-in",
  component: MFAChallenge,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  parameters: scenario(pendingMFA, reject("mfa")),
} satisfies Meta<typeof MFAChallenge>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Authenticator: Story = {};
export const RecoveryCode: Story = {
  play: async ({ canvasElement }) => {
    // The mode action is the first button after the form, independently of copy.
    await userEvent.click(canvasElement.querySelector("form + div button")!);
    await expect(
      canvasElement.querySelector('input[name="code"]'),
    ).toBeVisible();
  },
};
export const InvalidCode: Story = {
  play: async ({ canvasElement }) => {
    await authenticationCode(canvasElement);
    const canvas = within(canvasElement);
    await waitFor(() =>
      expect(canvas.getByRole("group")).toHaveAccessibleDescription(/\S/),
    );
    await expect(canvas.getAllByRole("textbox")[0]).toHaveFocus();
    await expect(canvas.getAllByRole("textbox")[0]).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    await expect(canvas.queryByRole("alert")).not.toBeInTheDocument();
  },
};
export const InvalidRecoveryCode: Story = {
  play: async (context) => {
    await RecoveryCode.play?.(context);
    const input = await fill(
      context.canvasElement,
      "code",
      "invalid-recovery-code",
    );
    await submit(context.canvasElement);
    await waitFor(() => expect(input).toHaveAccessibleDescription(/\S/));
    await expect(input).toHaveFocus();
    await expect(input).toHaveAttribute("aria-invalid", "true");
    await expect(
      within(context.canvasElement).queryByRole("alert"),
    ).not.toBeInTheDocument();
  },
};
export const RateLimited: Story = {
  parameters: scenario(pendingMFA, reject("mfa", "rate_limited", 429)),
  play: async ({ canvasElement }) => {
    await authenticationCode(canvasElement);
    const canvas = within(canvasElement);
    const notification = await within(
      canvasElement.ownerDocument.body,
    ).findByRole("dialog");
    await waitFor(() => expect(notification).toBeVisible(), { timeout: 5000 });
    await expect(canvas.queryByRole("alert")).not.toBeInTheDocument();
    await expect(canvas.getAllByRole("textbox")[0]).not.toHaveAttribute(
      "aria-invalid",
      "true",
    );
  },
};
export const Verifying: Story = {
  parameters: scenario(pendingMFA, submitting("mfa")),
  play: async ({ canvasElement }) => {
    await authenticationCode(canvasElement);
    await expect(
      canvasElement.querySelector('button[type="submit"]'),
    ).toBeDisabled();
  },
};
export const ValidationError: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.click(
      canvasElement.querySelector('button[type="submit"]')!,
    );
    await expect(
      within(canvasElement).getAllByRole("textbox")[0],
    ).toHaveAttribute("aria-invalid", "true");
  },
};
