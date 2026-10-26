import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, within } from "storybook/test";
import {
  accept,
  guest,
  member,
  reject,
  scenario,
  submitting,
} from "../../workshop/fixtures";
import { submit } from "../../workshop/interactions";
import { AuthFrame } from "./auth-frame";
import { EmailVerification } from "./email-verification";

const unverified = {
  ...member,
  user: { ...member.user!, emailVerified: false },
};
const verified = http.post("/api/v1/auth/email/verify", () =>
  HttpResponse.json(member),
);
const meta = {
  title: "Pages/Email verification",
  component: EmailVerification,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  args: { token: "workshop-email-verification-token" },
  parameters: scenario(
    unverified,
    accept("email/validate"),
    verified,
    accept("email/request"),
  ),
} satisfies Meta<typeof EmailVerification>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Verify: Story = {};
export const SignInRequired: Story = { parameters: scenario(guest) };
export const Request: Story = { args: { token: undefined } };
export const CheckingLink: Story = {
  parameters: scenario(unverified, submitting("email/validate")),
};
export const InvalidLink: Story = {
  parameters: scenario(
    unverified,
    reject("email/validate"),
    accept("email/request"),
  ),
};
export const CheckUnavailable: Story = {
  parameters: scenario(
    unverified,
    http.post("/api/v1/auth/email/validate", () => HttpResponse.error()),
  ),
};
export const ExpiredDuringEntry: Story = {
  parameters: scenario(
    unverified,
    accept("email/validate"),
    reject("email/verify"),
  ),
  play: async ({ canvasElement }) => {
    await within(within(canvasElement).getByRole("main")).findByRole("button");
    await submit(canvasElement);
    await expect(await within(canvasElement).findByRole("alert")).toBeVisible();
  },
};
export const EmailSent: Story = {
  ...Request,
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(within(canvasElement).getByRole("main")).getByRole("button"),
    );
    await expect(
      await within(canvasElement).findByRole("status"),
    ).toBeVisible();
  },
};
export const Complete: Story = { parameters: scenario(member) };
export const Verifying: Story = {
  parameters: scenario(
    unverified,
    accept("email/validate"),
    submitting("email/verify"),
  ),
  play: async ({ canvasElement }) => {
    await within(within(canvasElement).getByRole("main")).findByRole("button");
    await submit(canvasElement);
  },
};
export const RequestFailure: Story = {
  ...Request,
  parameters: scenario(
    unverified,
    http.post("/api/v1/auth/email/request", () => HttpResponse.error()),
  ),
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(within(canvasElement).getByRole("main")).getByRole("button"),
    );
  },
};
