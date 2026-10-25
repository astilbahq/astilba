import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { AuthFrame } from "../features/auth/auth-frame";
import { Button } from "./button";
import { MenuRoot, MenuTrigger, MenuContent, MenuItem } from "./menu";

const meta = {
  title: "Controls/Menu",
  component: MenuRoot,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  render: (args) => (
    <MenuRoot {...args}>
      <MenuTrigger render={<Button>Workspace actions</Button>} />
      <MenuContent aria-label="Workspace actions">
        <MenuItem>Workspace settings</MenuItem>
        <MenuItem>Invite a member</MenuItem>
        <MenuItem disabled>Delete workspace</MenuItem>
      </MenuContent>
    </MenuRoot>
  ),
} satisfies Meta<typeof MenuRoot>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Closed: Story = {};
export const Open: Story = { args: { defaultOpen: true } };
