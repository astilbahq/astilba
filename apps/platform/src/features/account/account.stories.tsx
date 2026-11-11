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
    await waitFor(() => expect(notification).toBeVisible());
    await expect(password).toHaveValue("a long example password");
  },
};
// A stale session confirms when the change is submitted, then the change goes through.
let passwordChanges = 0;
let confirmations: unknown[] = [];
const staleEnabled: Session = { ...enabled, recentAuth: false };
const confirmation = (canvas: HTMLElement) =>
  within(canvas.ownerDocument.body).findByRole("alertdialog", {
    name: "Confirm your identity",
  });
export const ConfirmIdentity: Story = {
  beforeEach: () => {
    passwordChanges = 0;
    confirmations = [];
  },
  parameters: scenario(
    staleEnabled,
    http.post("/api/v1/auth/password", () => {
      passwordChanges++;
      return HttpResponse.json(enabled);
    }),
    http.post("/api/v1/auth/reauthenticate", async ({ request }) => {
      confirmations.push(await request.json());
      return HttpResponse.json(enabled);
    }),
  ),
  play: async (context) => {
    const { canvasElement } = context;
    await PasswordChange.play!(context);
    await userEvent.type(
      within(canvasElement).getByLabelText("New password", {}),
      "a long example password",
    );
    await userEvent.click(
      within(canvasElement).getByRole("button", { name: "Update password" }),
    );
    const dialog = await confirmation(canvasElement);
    // Known to be stale, so nothing is sent before the confirmation.
    await expect(passwordChanges).toBe(0);
    await waitFor(() =>
      expect(within(dialog).getAllByRole("textbox")[0]).toHaveFocus(),
    );
    await userEvent.keyboard("123456");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Confirm" }),
    );
    await expect(
      await within(canvasElement).findByText(
        "Password updated. Your other sessions have been signed out.",
      ),
    ).toBeVisible();
    await expect(passwordChanges).toBe(1);
    await expect(confirmations).toEqual([{ code: "123456" }]);
  },
};
export const ConfirmWithRecoveryCode: Story = {
  parameters: scenario(staleEnabled),
  play: async (context) => {
    const { canvasElement } = context;
    await PasswordChange.play!(context);
    await userEvent.type(
      within(canvasElement).getByLabelText("New password", {}),
      "a long example password",
    );
    await userEvent.click(
      within(canvasElement).getByRole("button", { name: "Update password" }),
    );
    const dialog = await confirmation(canvasElement);
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Use a recovery code" }),
    );
    const code = within(dialog).getByLabelText("Recovery code", {});
    await waitFor(() => expect(code).toHaveFocus());
    await userEvent.click(
      within(dialog).getByRole("button", {
        name: "Use your authenticator app",
      }),
    );
    await waitFor(() =>
      expect(within(dialog).getAllByRole("textbox")[0]).toHaveFocus(),
    );
  },
};
// Account security has its own way to confirm, for someone about to do several things.
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
// Confirmation opens over the disable dialog. Escape closes only the confirmation, and
// focus returns to the control that asked.
export const ConfirmationOverDialog: Story = {
  parameters: scenario(
    staleEnabled,
    reject("mfa/disable", "network_error", 503),
  ),
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
    const canvas = within(context.canvasElement);
    const body = within(context.canvasElement.ownerDocument.body);
    const dialog = body.getByRole("alertdialog");
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
        within(dialog).getByRole("button", { name: "Disable two-factor" }),
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
    await waitFor(() => expect(body.queryByRole("alertdialog")).toBeNull());
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
let passwordAttempts = 0;
// A refusal the cached session did not predict opens the confirmation, keeps the draft and
// retries once. This server refuses again, so the second refusal is shown, not retried.
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
    const dialog = await confirmation(context.canvasElement);
    await userEvent.type(
      within(dialog).getByLabelText("Password"),
      "existing example password",
    );
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Confirm" }),
    );
    await expect(
      await within(context.canvasElement).findByRole("alert"),
    ).toHaveTextContent("Confirm your identity to continue.");
    await expect(
      within(context.canvasElement).getByLabelText("New password", {}),
    ).toHaveValue("a long preserved password");
    await expect(passwordAttempts).toBe(2);
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
// Declining after a refusal mid-enrollment returns focus to the enable button once it
// is usable again.
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
  },
};
