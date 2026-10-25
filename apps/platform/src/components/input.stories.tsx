import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { AuthFrame } from "../features/auth/auth-frame";
import * as s from "../styles";
import { Input } from "./input";
import { Label } from "./label";

const meta = {
  title: "Controls/Input",
  component: Input,
  decorators: [
    (Story) => (
      <AuthFrame>
        <div className={s.stack}>
          <Label htmlFor="example-input">Email</Label>
          <Story />
        </div>
      </AuthFrame>
    ),
  ],
  args: {
    id: "example-input",
    name: "email",
    type: "email",
    placeholder: "you@example.com",
  },
  argTypes: {
    size: { control: "select", options: ["sm", "md"] },
  },
} satisfies Meta<typeof Input>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Default: Story = {};
export const Invalid: Story = {
  args: { "aria-invalid": true, defaultValue: "unfinished" },
};
export const Disabled: Story = { args: { disabled: true } };
