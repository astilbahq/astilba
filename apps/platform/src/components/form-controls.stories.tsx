import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { AuthFrame } from "../features/auth/auth-frame";
import * as s from "../styles";
import { Checkbox } from "./checkbox";
import { Label } from "./label";
import { Select } from "./select";
import { Textarea } from "./textarea";

const meta = {
  title: "Controls/Label and native controls",
  decorators: [
    (Story) => (
      <AuthFrame>
        <div className={s.stack}>
          <Story />
        </div>
      </AuthFrame>
    ),
  ],
} satisfies Meta;
export default meta;
type Story = StoryObj<typeof meta>;
export const SelectField: Story = {
  render: () => (
    <>
      <Label htmlFor="region">Region</Label>
      <Select id="region" defaultValue="eu">
        <option value="eu">Europe</option>
        <option value="us">United States</option>
      </Select>
    </>
  ),
};
export const TextareaField: Story = {
  render: () => (
    <>
      <Label htmlFor="description">Description</Label>
      <Textarea id="description" placeholder="Describe this workspace" />
    </>
  ),
};
export const CheckboxField: Story = {
  render: () => (
    <Label>
      <Checkbox defaultChecked /> Keep me informed about workspace changes
    </Label>
  ),
};
