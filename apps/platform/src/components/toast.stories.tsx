import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { AuthFrame } from "../features/auth/auth-frame";
import { Button } from "./button";
import { useToast } from "./toast";

function ToastExample({
  type = "info",
}: {
  type?: "info" | "success" | "error";
}) {
  const { add } = useToast();
  return (
    <Button
      onClick={() =>
        add({
          type,
          description:
            type === "error"
              ? "The request could not be completed. Try again."
              : "Your workspace settings have been saved.",
          timeout: 0,
        })
      }
    >
      Show notification
    </Button>
  );
}

const meta = {
  title: "Controls/Toast",
  component: ToastExample,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
} satisfies Meta<typeof ToastExample>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole("button", { name: "Show notification" }),
    );
    const notification = await within(
      canvasElement.ownerDocument.body,
    ).findByRole("dialog");
    await waitFor(() => expect(notification).toBeVisible(), { timeout: 5000 });
  },
};
export const Success: Story = { args: { type: "success" }, play: Default.play };
export const Error: Story = { args: { type: "error" }, play: Default.play };
export const Multiple: Story = {
  play: async (context) => {
    await Default.play?.(context);
    const trigger = within(context.canvasElement).getByRole("button", {
      name: "Show notification",
    });
    await userEvent.click(trigger);
    await userEvent.click(trigger);
    await expect(
      within(context.canvasElement.ownerDocument.body).getAllByRole("dialog"),
    ).toHaveLength(3);
  },
};
