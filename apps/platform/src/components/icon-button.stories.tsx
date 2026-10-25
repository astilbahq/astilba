import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { AuthFrame } from "../features/auth/auth-frame";
import { IconButton } from "./icon-button";
import { CheckIcon } from "./icons";

const meta = {
  title: "Controls/Icon button",
  component: IconButton,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  args: {
    "aria-label": "Confirm selection",
    children: <CheckIcon />,
    variant: "ghost",
    size: "sm",
  },
  argTypes: {
    children: { control: false },
    variant: {
      control: "select",
      options: ["ghost", "primary", "secondary", "danger"],
    },
    size: { control: "select", options: ["sm", "md"] },
  },
} satisfies Meta<typeof IconButton>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Ghost: Story = {};
export const Primary: Story = { args: { variant: "primary" } };
export const Disabled: Story = { args: { disabled: true } };
