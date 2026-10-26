import type { Meta, StoryObj } from "@storybook/tanstack-react";
import type { QueryClient } from "@tanstack/react-query";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { css } from "../../../styled-system/css";
import { setSession, type Session, type Schema } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { member, reject, scenario } from "../../workshop/fixtures";
import { Account } from "./account";

const enabled: Session = { ...member, recentAuth: true };
const disabled: Session = {
  ...enabled,
  user: { ...enabled.user!, mfaEnabled: false },
};
const replacement = {
  generation: "00000000-0000-4000-8000-000000000005",
  codes: Array.from(
    { length: 10 },
    (_, index) => `EXAMPLE${String(index + 1).padStart(2, "0")}RECOVERYCODE`,
  ),
  session: {
    ...enabled,
    user: { ...enabled.user!, recoveryCodesSaved: false },
  },
};
const enrollment = http.post("/api/v1/auth/mfa/enroll", () =>
  HttpResponse.json({
    secret: "JBSWY3DPEHPK3PXP",
    uri: "otpauth://totp/Astilba:alex%40example.test?secret=JBSWY3DPEHPK3PXP&issuer=Astilba",
  }),
);

function sessionMutation(
  initial: Session,
  next: Session,
  path: string,
  response: Session | Schema["RecoveryCodes"] = next,
) {
  let current = initial;
  const parameters = scenario(
    initial,
    http.post(`/api/v1/auth/${path}`, () => {
      current = next;
      return HttpResponse.json(response);
    }),
  );
  parameters.msw.handlers.session.unshift(
    http.get("/api/v1/session", () => HttpResponse.json(current)),
  );
  return {
    parameters,
    beforeEach: () => {
      current = initial;
    },
  };
}

const meta = {
  title: "Pages/Account security",
  component: Account,
  decorators: [
    (Story) => (
      <main
        id="main"
        className={css({
          padding: { base: "4", sm: "8" },
          minBlockSize: "viewport",
          backgroundColor: "page",
        })}
      >
        <Story />
      </main>
    ),
  ],
  parameters: scenario(enabled),
} satisfies Meta<typeof Account>;
export default meta;
type Story = StoryObj<typeof meta>;

export const AuthenticatorEnabled: Story = {};
export const AuthenticatorDisabled: Story = {
  parameters: scenario(disabled, enrollment),
};
export const UnverifiedEmail: Story = {
  parameters: scenario({
    ...enabled,
    user: { ...enabled.user!, emailVerified: false },
  }),
};
export const PasswordChange: Story = {
  parameters: scenario(disabled),
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole("button", {
        name: "Change password",
      }),
    );
    await expect(
      within(canvasElement).getByLabelText("New password", {}),
    ).toBeVisible();
  },
};
export const PasswordValidation: Story = {
  ...PasswordChange,
  play: async (context) => {
    await PasswordChange.play!(context);
    const password = within(context.canvasElement).getByLabelText(
      "New password",
      {},
    );
    await userEvent.type(password, "short");
    await userEvent.click(
      within(context.canvasElement).getByRole("button", {
        name: "Update password",
      }),
    );
    await expect(password).toHaveAccessibleDescription(
      "Use at least 15 characters for your password.",
    );
    await expect(password).toHaveFocus();
  },
};
export const PasswordUpdated: Story = {
  ...sessionMutation(disabled, disabled, "password"),
  play: async (context) => {
    await PasswordChange.play!(context);
    await userEvent.type(
      within(context.canvasElement).getByLabelText("New password", {}),
      "a long example password",
    );
    await userEvent.click(
      within(context.canvasElement).getByRole("button", {
        name: "Update password",
      }),
    );
    await expect(
      await within(context.canvasElement).findByText(
        "Password updated. Your other sessions have been signed out.",
      ),
    ).toHaveAttribute("role", "status");
    await expect(
      within(context.canvasElement).queryByLabelText("New password", {}),
    ).toBeNull();
    await expect(
      within(context.canvasElement).getByRole("heading", {
        name: "Password",
      }),
    ).toHaveFocus();
  },
};
export const PasswordRequestFailure: Story = {
  parameters: scenario(disabled, reject("password", "network_error", 503)),
  play: async (context) => {
    await PasswordChange.play!(context);
    const password = within(context.canvasElement).getByLabelText(
      "New password",
      {},
    );
    await userEvent.type(password, "a long example password");
    await userEvent.click(
      within(context.canvasElement).getByRole("button", {
        name: "Update password",
      }),
    );
    const notification = await within(
      context.canvasElement.ownerDocument.body,
    ).findByRole("dialog");
    await waitFor(() => expect(notification).toBeVisible(), { timeout: 5000 });
    await expect(password).toHaveValue("a long example password");
  },
};
export const ConfirmIdentity: Story = {
  parameters: scenario({ ...enabled, recentAuth: false }),
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole("button", {
        name: "Change password",
      }),
    );
    await waitFor(() =>
      expect(
        within(canvasElement).getByRole("heading", {
          name: "Confirm your identity",
        }),
      ).toHaveFocus(),
    );
  },
};
export const ConfirmWithRecoveryCode: Story = {
  ...ConfirmIdentity,
  play: async (context) => {
    await ConfirmIdentity.play!(context);
    await userEvent.click(
      within(context.canvasElement).getByRole("button", {
        name: "Use recovery code",
      }),
    );
    await expect(
      within(context.canvasElement).getByLabelText("Recovery code", {}),
    ).toBeVisible();
  },
};
export const ScanAuthenticator: Story = {
  ...AuthenticatorDisabled,
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole("button", {
        name: "Set up authenticator",
      }),
    );
    await expect(
      await within(canvasElement).findByRole("group", {
        name: "Authentication code",
      }),
    ).toBeVisible();
  },
};
export const ConfirmDisable: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole("button", {
        name: "Disable two-factor authentication",
      }),
    );
    const dialog = await within(canvasElement.ownerDocument.body).findByRole(
      "alertdialog",
    );
    await expect(dialog).toHaveAccessibleName(
      "Disable two-factor authentication?",
    );
    await waitFor(() => expect(dialog).toBeVisible(), { timeout: 5000 });
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "Cancel" }),
      ).toHaveFocus(),
    );
  },
};
export const CancelDisable: Story = {
  play: async (context) => {
    await ConfirmDisable.play!(context);
    const body = within(context.canvasElement.ownerDocument.body);
    const dialog = body.getByRole("alertdialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Cancel" }),
    );
    await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull(), {
      timeout: 5000,
    });
    await expect(
      within(context.canvasElement).getByRole("button", {
        name: "Disable two-factor authentication",
      }),
    ).toHaveFocus();
  },
};
export const DisableFailed: Story = {
  parameters: scenario(enabled, reject("mfa/disable", "network_error", 503)),
  play: async (context) => {
    await ConfirmDisable.play!(context);
    const dialog = within(context.canvasElement.ownerDocument.body).getByRole(
      "alertdialog",
    );
    await userEvent.click(
      within(dialog).getByRole("button", {
        name: "Disable two-factor",
      }),
    );
    await expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "The request could not be completed. Try again.",
    );
    await expect(dialog).toBeVisible();
  },
};
export const DisabledSuccessfully: Story = {
  ...sessionMutation(enabled, disabled, "mfa/disable"),
  play: async (context) => {
    await ConfirmDisable.play!(context);
    const body = within(context.canvasElement.ownerDocument.body);
    await userEvent.click(
      within(body.getByRole("alertdialog")).getByRole("button", {
        name: "Disable two-factor",
      }),
    );
    await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull(), {
      timeout: 5000,
    });
    await expect(
      within(context.canvasElement).getByText(
        "Two-factor authentication disabled.",
      ),
    ).toHaveAttribute("role", "status");
    await expect(
      within(context.canvasElement).getByRole("heading", {
        name: "Two-factor authentication",
      }),
    ).toHaveFocus();
  },
};
export const SaveRecoveryCodes: Story = {
  ...sessionMutation(
    enabled,
    replacement.session,
    "mfa/recovery-codes",
    replacement,
  ),
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole("button", {
        name: "Replace recovery codes",
      }),
    );
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      within(await body.findByRole("alertdialog")).getByRole("button", {
        name: "Replace codes",
      }),
    );
    await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull(), {
      timeout: 5000,
    });
    await expect(
      await within(canvasElement).findByRole("list", {
        name: "Recovery codes",
      }),
    ).toBeVisible();
  },
};
export const AccountChangedWithRecoveryCodes: Story = {
  ...SaveRecoveryCodes,
  play: async (context) => {
    await SaveRecoveryCodes.play!(context);
    const queries = context.loaded.workshop.queryClient as QueryClient;
    queries.setQueryData(
      sessionOptions.queryKey,
      setSession({
        ...enabled,
        user: {
          ...enabled.user!,
          id: "00000000-0000-4000-8000-000000000009",
          email: "another@example.test",
        },
      }),
    );
    await expect(
      await within(context.canvasElement).findByText("another@example.test"),
    ).toBeVisible();
    await expect(
      within(context.canvasElement).queryByRole("list", {
        name: "Recovery codes",
      }),
    ).toBeNull();
  },
};
let passwordAttempts = 0;
export const ExpiredIdentityKeepsPasswordDraft: Story = {
  beforeEach: () => {
    passwordAttempts = 0;
  },
  parameters: scenario(
    disabled,
    http.post("/api/v1/auth/password", () => {
      passwordAttempts++;
      return HttpResponse.json(
        {
          code: "recent_auth_required",
          message: "Workshop response.",
          requestId: "workshop-request",
        },
        { status: 403 },
      );
    }),
    http.post("/api/v1/auth/reauthenticate", () => HttpResponse.json(disabled)),
  ),
  play: async (context) => {
    await PasswordChange.play!(context);
    await userEvent.type(
      within(context.canvasElement).getByLabelText("New password", {}),
      "a long preserved password",
    );
    await userEvent.click(
      within(context.canvasElement).getByRole("button", {
        name: "Update password",
      }),
    );
    await userEvent.type(
      await within(context.canvasElement).findByLabelText(
        "Current password",
        {},
      ),
      "existing example password",
    );
    await userEvent.click(
      within(context.canvasElement).getByRole("button", {
        name: "Confirm identity",
      }),
    );
    await expect(
      await within(context.canvasElement).findByLabelText("New password", {}),
    ).toHaveValue("a long preserved password");
    await expect(passwordAttempts).toBe(1);
  },
};
export const BackgroundExpiryKeepsUnsubmittedDraft: Story = {
  parameters: scenario(
    disabled,
    http.post("/api/v1/auth/reauthenticate", () => HttpResponse.json(disabled)),
  ),
  play: async (context) => {
    await PasswordChange.play!(context);
    await userEvent.type(
      within(context.canvasElement).getByLabelText("New password"),
      "a current unsent password",
    );
    const queries = context.loaded.workshop.queryClient as QueryClient;
    queries.setQueryData(sessionOptions.queryKey, {
      ...disabled,
      recentAuth: false,
    });
    await userEvent.type(
      await within(context.canvasElement).findByLabelText("Current password"),
      "existing example password",
    );
    await userEvent.click(
      within(context.canvasElement).getByRole("button", {
        name: "Confirm identity",
      }),
    );
    await expect(
      await within(context.canvasElement).findByLabelText("New password"),
    ).toHaveValue("a current unsent password");
  },
};
export const AuthenticatorIdentityExpired: Story = {
  parameters: scenario(
    disabled,
    enrollment,
    reject("mfa/confirm", "recent_auth_required", 403),
  ),
  play: async (context) => {
    await ScanAuthenticator.play!(context);
    const first = within(context.canvasElement).getAllByRole("textbox")[0];
    await userEvent.type(first, "123456");
    await userEvent.click(
      within(context.canvasElement).getByRole("button", {
        name: "Enable two-factor authentication",
      }),
    );
    await waitFor(() =>
      expect(
        within(context.canvasElement).getByRole("heading", {
          name: "Confirm your identity",
        }),
      ).toHaveFocus(),
    );
  },
};
