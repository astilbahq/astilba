import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent } from "storybook/test";
import { AuthFrame } from "../features/auth/auth-frame";
import * as s from "../styles";
import { Label } from "./label";
import { PasswordInput } from "./password-input";

const meta = {
  title: "Controls/Password input",
  component: PasswordInput,
  decorators: [
    (Story) => (
      <AuthFrame>
        <div className={s.stack}>
          <Label htmlFor="example-password">Password</Label>
          <Story />
        </div>
      </AuthFrame>
    ),
  ],
  args: {
    id: "example-password",
    placeholder: "Enter your password",
    defaultValue: "correct horse battery staple",
  },
} satisfies Meta<typeof PasswordInput>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Hidden: Story = {};
export const Revealed: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.click(
      canvasElement.querySelector('button[aria-controls="example-password"]')!,
    );
    await expect(canvasElement.querySelector("input")).toHaveAttribute(
      "type",
      "text",
    );
  },
};
export const Disabled: Story = { args: { disabled: true } };
