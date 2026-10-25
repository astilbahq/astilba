import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent, within } from "storybook/test";
import { AuthFrame } from "../features/auth/auth-frame";
import { IconButton } from "./icon-button";
import { CheckIcon } from "./icons";
import { Tooltip } from "./tooltip";

const meta = {
  title: "Controls/Tooltip",
  component: Tooltip,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  args: {
    content: "Confirm selection",
    children: (
      <IconButton aria-label="Preview confirmation">
        <CheckIcon />
      </IconButton>
    ),
  },
  argTypes: { children: { control: false } },
} satisfies Meta<typeof Tooltip>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Default: Story = {};
export const Visible: Story = {
  play: async ({ canvasElement }) => {
    const trigger = within(canvasElement).getByRole("button", {
      name: "Preview confirmation",
    });
    for (
      let index = 0;
      index < 3 && canvasElement.ownerDocument.activeElement !== trigger;
      index++
    ) {
      await userEvent.tab();
    }
    await expect(trigger).toHaveFocus();
    await within(canvasElement.ownerDocument.body).findByText(
      "Confirm selection",
    );
  },
};
