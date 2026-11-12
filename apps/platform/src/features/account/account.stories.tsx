import type { Meta, StoryObj } from "@storybook/tanstack-react";
import {
  defaultScheduler,
  notifyManager,
  type QueryClient,
} from "@tanstack/react-query";
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

// What the workshop server was sent, reset before each story.
let sent: unknown[] = [];
function sessionMutation(
  initial: Session,
  next: Session,
  path: string,
  response: Session | Schema["RecoveryCodes"] = next,
) {
  let current = initial;
  const parameters = scenario(
    initial,
    http.post(`/api/v1/auth/${path}`, async ({ request }) => {
      sent.push(await request.json());
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
  beforeEach: () => {
    sent = [];
  },
} satisfies Meta<typeof Account>;
export default meta;
type Story = StoryObj<typeof meta>;

// Changes to how the person signs in ask for the current credential in this dialog.
const confirmation = (canvas: HTMLElement) =>
  within(canvas.ownerDocument.body).findByRole("alertdialog", {
    name: "Confirm your identity",
  });
// Answers the dialog with the password, or with an authenticator code when given none.
async function confirmWith(canvas: HTMLElement, password?: string) {
  const dialog = await confirmation(canvas);
  const input = password
    ? within(dialog).getByLabelText("Password")
    : within(dialog).getAllByRole("textbox")[0];
  await waitFor(() => expect(input).toHaveFocus());
  await userEvent.keyboard(password ?? "123456");
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Confirm" }),
  );
  return dialog;
}
const confirmationClosed = (canvas: HTMLElement) =>
  waitFor(() =>
    expect(
      within(canvas.ownerDocument.body).queryByRole("alertdialog", {
        name: "Confirm your identity",
      }),
    ).toBeNull(),
  );

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
async function submitNewPassword(
  context: Parameters<NonNullable<Story["play"]>>[0],
  password = "a long example password",
) {
  await PasswordChange.play!(context);
  await userEvent.type(
    within(context.canvasElement).getByLabelText("New password", {}),
    password,
  );
  await userEvent.click(
    within(context.canvasElement).getByRole("button", {
      name: "Update password",
    }),
  );
}
// The new password is sent with the current one, typed in the confirmation.
export const PasswordUpdated: Story = {
  ...sessionMutation(disabled, disabled, "password"),
  play: async (context) => {
    await submitNewPassword(context);
    await expect(
      await confirmation(context.canvasElement),
    ).toHaveAccessibleDescription(
      "Changing how you sign in needs your current credential. Enter your current password.",
    );
    await confirmWith(context.canvasElement, "existing example password");
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
    await expect(sent).toEqual([
      {
        password: "a long example password",
        current: { password: "existing example password" },
      },
    ]);
  },
};
export const PasswordRequestFailure: Story = {
  parameters: scenario(disabled, reject("password", "network_error", 503)),
  // A failure of the change itself closes the confirmation and shows on the screen, which
  // keeps the draft.
  play: async (context) => {
    await submitNewPassword(context);
    await confirmWith(context.canvasElement, "existing example password");
    await confirmationClosed(context.canvasElement);
    const password = within(context.canvasElement).getByLabelText(
      "New password",
      {},
    );
    const notification = await within(
      context.canvasElement.ownerDocument.body,
    ).findByRole("dialog");
    await waitFor(() => expect(notification).toBeVisible());
    await expect(password).toHaveValue("a long example password");
  },
};
// A wrong current password stays in the confirmation to try again, over the kept draft.
export const WrongCurrentPassword: Story = {
  parameters: scenario(
    disabled,
    reject("password", "invalid_credentials", 400),
  ),
  play: async (context) => {
    await submitNewPassword(context);
    const dialog = await confirmWith(context.canvasElement, "not the password");
    await waitFor(() =>
      expect(
        within(dialog).getByLabelText("Password"),
      ).toHaveAccessibleDescription("This password is incorrect. Try again."),
    );
    await expect(dialog).toBeVisible();
    await expect(
      within(context.canvasElement).getByLabelText("New password", {}),
    ).toHaveValue("a long example password");
  },
};
// Two-factor accounts can switch between kinds of code, and either is sent as `current`.
export const ConfirmWithRecoveryCode: Story = {
  ...sessionMutation(enabled, enabled, "password"),
  play: async (context) => {
    const { canvasElement } = context;
    await submitNewPassword(context);
    const dialog = await confirmation(canvasElement);
    const switchToRecovery = () =>
      userEvent.click(
        within(dialog).getByRole("button", { name: "Use a recovery code" }),
      );
    await switchToRecovery();
    await waitFor(() =>
      expect(within(dialog).getByLabelText("Recovery code", {})).toHaveFocus(),
    );
    await userEvent.click(
      within(dialog).getByRole("button", {
        name: "Use your authenticator app",
      }),
    );
    await waitFor(() =>
      expect(within(dialog).getAllByRole("textbox")[0]).toHaveFocus(),
    );
    await switchToRecovery();
    const code = within(dialog).getByLabelText("Recovery code", {});
    await waitFor(() => expect(code).toHaveFocus());
    await expect(dialog).toHaveAccessibleDescription(
      /^Changing how you sign in needs your current credential\. .*recovery codes/,
    );
    await userEvent.type(code, " EXAMPLE01RECOVERYCODE ");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Confirm" }),
    );
    await expect(
      await within(canvasElement).findByText(
        "Password updated. Your other sessions have been signed out.",
      ),
    ).toBeVisible();
    await expect(sent).toEqual([
      {
        password: "a long example password",
        current: { code: "EXAMPLE01RECOVERYCODE" },
      },
    ]);
  },
};
// Account security has its own way to confirm, for someone about to do several things.
const staleEnabled: Session = { ...enabled, recentAuth: false };
export const StandaloneConfirmation: Story = {
  ...sessionMutation(staleEnabled, enabled, "reauthenticate"),
  play: async (context) => {
    const { canvasElement } = context;
    const button = within(canvasElement).getByRole("button", {
      name: "Confirm identity",
    });
    await userEvent.click(button);
    const dialog = await confirmation(canvasElement);
    await waitFor(() =>
      expect(within(dialog).getAllByRole("textbox")[0]).toHaveFocus(),
    );
    await userEvent.keyboard("123456");
    await userEvent.keyboard("{Enter}");
    await expect(
      await within(canvasElement).findByText("Identity confirmed."),
    ).toHaveAttribute("role", "status");
    await waitFor(() => expect(button).toHaveFocus());
    // The status stays once the session has been read again after confirming.
    const queries = context.loaded.workshop.queryClient as QueryClient;
    await waitFor(() => expect(queries.isFetching()).toBe(0));
    await expect(
      within(canvasElement).getByText("Identity confirmed."),
    ).toBeVisible();
  },
};
// Confirmation opens over the disable dialog. Escape closes only the confirmation, sends
// nothing, and focus returns to the control that asked.
export const ConfirmationOverDialog: Story = {
  ...sessionMutation(enabled, disabled, "mfa/disable"),
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole("button", {
        name: "Disable two-factor authentication",
      }),
    );
    const body = within(canvasElement.ownerDocument.body);
    const disable = await body.findByRole("alertdialog", {
      name: "Disable two-factor authentication?",
    });
    const confirm = within(disable).getByRole("button", {
      name: "Disable two-factor",
    });
    await userEvent.click(confirm);
    const stacked = await confirmation(canvasElement);
    await waitFor(() =>
      expect(within(stacked).getAllByRole("textbox")[0]).toHaveFocus(),
    );
    await userEvent.keyboard("{Escape}");
    await waitFor(() =>
      expect(
        body.queryByRole("alertdialog", { name: "Confirm your identity" }),
      ).toBeNull(),
    );
    await expect(disable).toBeVisible();
    await waitFor(() => expect(confirm).toHaveFocus());
    await expect(within(disable).queryByRole("alert")).toBeNull();
    await expect(sent).toEqual([]);
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
    await waitFor(() => expect(dialog).toBeVisible());
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
    await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull());
    await waitFor(() =>
      expect(
        within(context.canvasElement).getByRole("button", {
          name: "Disable two-factor authentication",
        }),
      ).toHaveFocus(),
    );
  },
};
export const DisableFailed: Story = {
  parameters: scenario(enabled, reject("mfa/disable", "network_error", 503)),
  play: async (context) => {
    await ConfirmDisable.play!(context);
    const dialog = within(context.canvasElement.ownerDocument.body).getByRole(
      "alertdialog",
    );
    const confirm = within(dialog).getByRole("button", {
      name: "Disable two-factor",
    });
    await userEvent.click(confirm);
    // The change's own failure closes the confirmation and shows in the dialog that asked.
    await confirmWith(context.canvasElement);
    await confirmationClosed(context.canvasElement);
    await expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "The request could not be completed. Try again.",
    );
    await expect(dialog).toBeVisible();
    await waitFor(() => expect(confirm).toHaveFocus());
  },
};
export const DisabledSuccessfully: Story = {
  ...sessionMutation(enabled, disabled, "mfa/disable"),
  play: async (context) => {
    await ConfirmDisable.play!(context);
    const canvas = within(context.canvasElement);
    const body = within(context.canvasElement.ownerDocument.body);
    const dialog = body.getByRole("alertdialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Disable two-factor" }),
    );
    const stacked = await confirmation(context.canvasElement);
    await waitFor(() =>
      expect(within(stacked).getAllByRole("textbox")[0]).toHaveFocus(),
    );
    await userEvent.keyboard("123456");
    const notifications: Array<() => void> = [];
    const resumeNotifications = () => {
      notifyManager.setScheduler(defaultScheduler);
      notifications.splice(0).forEach(defaultScheduler);
    };
    // The dialog can close before session observers remove its trigger.
    notifyManager.setScheduler((callback) => {
      notifications.push(callback);
    });
    try {
      await userEvent.click(
        within(stacked).getByRole("button", { name: "Confirm" }),
      );
      await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull());
      await waitFor(() => expect(dialog).not.toBeInTheDocument());
      resumeNotifications();
      await expect(
        await canvas.findByRole("button", { name: "Set up authenticator" }),
      ).toBeVisible();
      await expect(
        canvas.getByText("Two-factor authentication disabled."),
      ).toHaveAttribute("role", "status");
      await waitFor(() =>
        expect(
          canvas.getByRole("heading", { name: "Two-factor authentication" }),
        ).toHaveFocus(),
      );
      await expect(sent).toEqual([{ current: { code: "123456" } }]);
    } finally {
      resumeNotifications();
    }
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
    await confirmWith(canvasElement);
    await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull());
    await expect(sent).toEqual([{ current: { code: "123456" } }]);
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
    await waitFor(() => expect(queries.isMutating()).toBe(0));
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
// Recent authentication lapsing in the background does not interrupt typing.
export const BackgroundExpiryKeepsUnsubmittedDraft: Story = {
  parameters: scenario(disabled),
  play: async (context) => {
    await PasswordChange.play!(context);
    const password = within(context.canvasElement).getByLabelText(
      "New password",
    );
    await userEvent.type(password, "a current unsent password");
    const queries = context.loaded.workshop.queryClient as QueryClient;
    queries.setQueryData(sessionOptions.queryKey, {
      ...disabled,
      recentAuth: false,
    });
    await userEvent.type(password, " still typing");
    await expect(password).toHaveValue(
      "a current unsent password still typing",
    );
    await expect(
      within(context.canvasElement.ownerDocument.body).queryByRole(
        "alertdialog",
      ),
    ).toBeNull();
  },
};
// Declining the confirmation mid-enrollment sends nothing, and returns focus to the enable
// button once it is usable again.
export const AuthenticatorConfirmationDeclined: Story = {
  parameters: scenario(
    disabled,
    enrollment,
    http.post("/api/v1/auth/mfa/confirm", async ({ request }) => {
      sent.push(await request.json());
      return HttpResponse.json(replacement);
    }),
  ),
  play: async (context) => {
    await ScanAuthenticator.play!(context);
    const first = within(context.canvasElement).getAllByRole("textbox")[0];
    await userEvent.type(first, "123456");
    const enable = within(context.canvasElement).getByRole("button", {
      name: "Enable two-factor authentication",
    });
    await userEvent.click(enable);
    const dialog = await confirmation(context.canvasElement);
    await expect(within(dialog).getByLabelText("Password")).toBeVisible();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Cancel" }),
    );
    await waitFor(() =>
      expect(
        within(context.canvasElement).getByRole("button", {
          name: "Enable two-factor authentication",
        }),
      ).toHaveFocus(),
    );
    await expect(sent).toEqual([]);
  },
};
