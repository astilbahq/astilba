import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { AuthFrame } from "../features/auth/auth-frame";
import { Link, LinkButton } from "./link";

const meta = {
  title: "Controls/Link",
  component: Link,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  args: { to: "/reset", children: "Forgot password?" },
} satisfies Meta<typeof Link>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Underlined: Story = {};
export const Plain: Story = { args: { variant: "plain" } };
export const Subtle: Story = { args: { variant: "subtle" } };
export const Action: Story = {
  render: () => <LinkButton>Use recovery code</LinkButton>,
};
