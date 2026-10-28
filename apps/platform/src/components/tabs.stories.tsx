import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { AuthFrame } from "../features/auth/auth-frame";
import { TabsRoot, TabsList, TabsTab, TabsPanel } from "./tabs";

function Example() {
  return (
    <TabsRoot defaultValue="overview">
      <TabsList aria-label="Example sections">
        <TabsTab value="overview">Overview</TabsTab>
        <TabsTab value="configuration">Configuration</TabsTab>
        <TabsTab value="activity" disabled>
          Activity
        </TabsTab>
      </TabsList>
      <TabsPanel value="overview">Environment overview.</TabsPanel>
      <TabsPanel value="configuration">Saved service configuration.</TabsPanel>
    </TabsRoot>
  );
}
const meta = {
  title: "Controls/Tabs",
  component: Example,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
} satisfies Meta<typeof Example>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Default: Story = {};
