import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent, within } from "storybook/test";
import { AuthFrame } from "../features/auth/auth-frame";
import * as s from "../styles";
import { Label } from "./label";
import { OTPInput } from "./otp-input";

const meta = {
  title: "Controls/OTP input",
  component: OTPInput,
  decorators: [
    (Story) => (
      <AuthFrame>
        <div className={s.stack}>
          <Label htmlFor="example-code">Authentication code</Label>
          <Story />
        </div>
      </AuthFrame>
    ),
  ],
  args: { id: "example-code", length: 6, name: "code" },
} satisfies Meta<typeof OTPInput>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Empty: Story = {};
export const Partial: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.type(
      within(canvasElement).getAllByRole("textbox")[0],
      "123",
    );
    await expect(
      within(canvasElement).getAllByRole("textbox")[3],
    ).toHaveFocus();
  },
};
export const Filled: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.type(
      within(canvasElement).getAllByRole("textbox")[0],
      "123456",
    );
  },
};
export const Disabled: Story = { args: { disabled: true } };
