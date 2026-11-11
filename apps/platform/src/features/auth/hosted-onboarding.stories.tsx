import type { Meta, StoryObj } from "@storybook/tanstack-react";
import type { QueryClient } from "@tanstack/react-query";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { setSession } from "../../api/client";
import { sessionOptions } from "../../api/session";
import {
  hostedMember,
  hostedScenario,
  hostedSubscription,
  hostedWorkspaceId,
  guest,
  scenario,
} from "../../workshop/fixtures";
import { authenticationCode } from "../../workshop/interactions";
import { AuthFrame } from "./auth-frame";
import { HostedOnboarding } from "./hosted-onboarding";

const enrolled = {
  ...hostedMember,
  user: { ...hostedMember.user!, recoveryCodesSaved: false },
};
const unprotected = {
  ...hostedMember,
  user: { ...hostedMember.user!, mfaEnabled: false },
};
const subscription = (values = {}) =>
  http.get("/api/v1/workspaces/:workspaceId/subscription", () =>
    HttpResponse.json({ ...hostedSubscription, ...values }),
  );
const generation = "00000000-0000-4000-8000-000000000003";
const codes = {
  generation,
  codes: Array.from(
    { length: 10 },
    (_, index) => `EXAMPLE${String(index + 1).padStart(2, "0")}RECOVERYCODE`,
  ),
  session: enrolled,
};
const regenerate = http.post("/api/v1/auth/mfa/recovery-codes", () =>
  HttpResponse.json(codes),
);
const enrollmentData = {
  secret: "JBSWY3DPEHPK3PXP",
  uri: "otpauth://totp/Astilba:alex%40example.test?secret=JBSWY3DPEHPK3PXP&issuer=Astilba",
};
const enrollment = http.post("/api/v1/auth/mfa/enroll", () =>
  HttpResponse.json(enrollmentData),
);
const meta = {
  title: "Pages/Hosted onboarding",
  component: HostedOnboarding,
  decorators: [
    (Story) => (
      <AuthFrame>
        <Story />
      </AuthFrame>
    ),
  ],
  args: { workspaceId: hostedWorkspaceId },
  parameters: hostedScenario(unprotected, subscription(), enrollment),
} satisfies Meta<typeof HostedOnboarding>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Authenticator: Story = {};
export const ScanAuthenticator: Story = {
  play: async ({ canvasElement }) => {
    const main = within(canvasElement).getByRole("main");
    await userEvent.click(await within(main).findByRole("button"));
    await expect(await within(main).findByRole("group")).toBeVisible();
  },
};
export const InvalidAuthenticatorCode: Story = {
  ...ScanAuthenticator,
  parameters: hostedScenario(
    unprotected,
    subscription(),
    enrollment,
    http.post("/api/v1/auth/mfa/confirm", () =>
      HttpResponse.json(
        {
          code: "invalid_credentials",
          message: "Workshop response.",
          requestId: "workshop-request",
        },
        { status: 400 },
      ),
    ),
  ),
  play: async (context) => {
    await ScanAuthenticator.play!(context);
    await authenticationCode(context.canvasElement);
    await waitFor(() =>
      expect(
        context.canvasElement.querySelector('[aria-invalid="true"]'),
      ).toBeInTheDocument(),
    );
  },
};
export const ReplaceMissingCodes: Story = {
  parameters: hostedScenario(enrolled, subscription(), regenerate),
};
export const SaveRecoveryCodes: Story = {
  ...ReplaceMissingCodes,
  play: async ({ canvasElement }) => {
    await userEvent.click(
      await within(within(canvasElement).getByRole("main")).findByRole(
        "button",
      ),
    );
    await waitFor(() =>
      expect(canvasElement.querySelector("li code")).toBeVisible(),
    );
  },
};
let releaseAcknowledgement: () => void;
let acknowledgementGate: Promise<void>;
let acknowledgementStarted = false;
let acknowledgementReturned = false;
let currentAccount = enrolled;
export const AccountChangedDuringAcknowledgement: Story = {
  ...SaveRecoveryCodes,
  beforeEach: () => {
    acknowledgementStarted = false;
    acknowledgementReturned = false;
    currentAccount = enrolled;
    acknowledgementGate = new Promise<void>((resolve) => {
      releaseAcknowledgement = resolve;
    });
    return () => releaseAcknowledgement();
  },
  parameters: (() => {
    const parameters = hostedScenario(
      enrolled,
      subscription(),
      regenerate,
      http.post("/api/v1/auth/mfa/recovery-codes/acknowledge", async () => {
        acknowledgementStarted = true;
        await acknowledgementGate;
        acknowledgementReturned = true;
        return HttpResponse.json(enrolled);
      }),
    );
    parameters.msw.handlers.session.unshift(
      http.get("/api/v1/session", () => HttpResponse.json(currentAccount)),
    );
    return parameters;
  })(),
  play: async (context) => {
    await SaveRecoveryCodes.play!(context);
    const queries = context.loaded.workshop.queryClient as QueryClient;
    await waitFor(() => expect(queries.isMutating()).toBe(0));
    await userEvent.click(
      within(context.canvasElement).getByRole("button", {
        name: "I have saved my codes",
      }),
    );
    await waitFor(() => expect(acknowledgementStarted).toBe(true));
    currentAccount = {
      ...enrolled,
      user: {
        ...enrolled.user,
        id: "00000000-0000-4000-8000-000000000004",
        email: "another@example.test",
      },
      workspaces: [],
    };
    queries.setQueryData(sessionOptions.queryKey, setSession(currentAccount));
    const seenAccounts: (string | undefined)[] = [];
    const stop = queries.getQueryCache().subscribe(() => {
      seenAccounts.push(
        queries.getQueryData(sessionOptions.queryKey)?.user?.id,
      );
    });
    try {
      await waitFor(() =>
        expect(context.canvasElement.querySelector("li code")).toBeNull(),
      );
      releaseAcknowledgement();
      await waitFor(() => expect(acknowledgementReturned).toBe(true));
      await waitFor(() => expect(queries.isMutating()).toBe(0));
      await expect(seenAccounts).not.toContain(enrolled.user.id);
      await expect(
        queries.getQueryData(sessionOptions.queryKey)?.user?.id,
      ).toBe(currentAccount.user.id);
    } finally {
      stop();
      releaseAcknowledgement();
    }
  },
};
export const ChangedRecoveryCodes: Story = {
  ...SaveRecoveryCodes,
  parameters: hostedScenario(
    enrolled,
    subscription(),
    regenerate,
    http.post("/api/v1/auth/mfa/recovery-codes/acknowledge", () =>
      HttpResponse.json(
        {
          code: "conflict",
          message: "Workshop response.",
          requestId: "workshop-request",
        },
        { status: 409 },
      ),
    ),
  ),
  play: async (context) => {
    await SaveRecoveryCodes.play!(context);
    await userEvent.click(
      context.canvasElement.querySelector<HTMLButtonElement>(
        'button[type="submit"]',
      )!,
    );
    await expect(
      await within(context.canvasElement).findByRole("alert"),
    ).toBeVisible();
    await expect(
      context.canvasElement.querySelector("li code"),
    ).not.toBeInTheDocument();
  },
};
// A stale session confirms in the shared dialog, and the step it was asked from continues.
let confirmations: unknown[] = [];
function staleScenario(
  session: typeof hostedMember,
  ...handlers: ReturnType<typeof http.post>[]
) {
  return {
    beforeEach: () => {
      confirmations = [];
    },
    parameters: hostedScenario(
      { ...session, recentAuth: false },
      subscription(),
      ...handlers,
      http.post("/api/v1/auth/reauthenticate", async ({ request }) => {
        confirmations.push(await request.json());
        return HttpResponse.json({ ...session, recentAuth: true });
      }),
    ),
  };
}
async function startStep(canvasElement: HTMLElement) {
  const main = within(canvasElement).getByRole("main");
  await userEvent.click(await within(main).findByRole("button"));
  return within(canvasElement.ownerDocument.body).findByRole("alertdialog", {
    name: "Confirm your identity",
  });
}
export const AuthenticatorSetupConfirmsIdentity: Story = {
  ...staleScenario(unprotected, enrollment),
  play: async ({ canvasElement }) => {
    const confirmation = await startStep(canvasElement);
    const password = within(confirmation).getByLabelText("Password");
    await waitFor(() => expect(password).toHaveFocus());
    await userEvent.type(password, "correct horse battery staple");
    await userEvent.keyboard("{Enter}");
    // The page is hidden from assistive technology until the confirmation has closed.
    await waitFor(() =>
      expect(
        within(canvasElement.ownerDocument.body).queryByRole("alertdialog"),
      ).toBeNull(),
    );
    const main = within(canvasElement).getByRole("main");
    await expect(await within(main).findByRole("group")).toBeVisible();
    await expect(confirmations).toEqual([
      { password: "correct horse battery staple" },
    ]);
  },
};
export const RecoveryCodesConfirmIdentity: Story = {
  ...staleScenario(enrolled, regenerate),
  play: async ({ canvasElement }) => {
    const confirmation = await startStep(canvasElement);
    await waitFor(() =>
      expect(within(confirmation).getAllByRole("textbox")[0]).toHaveFocus(),
    );
    await userEvent.keyboard("123456");
    await userEvent.click(
      within(confirmation).getByRole("button", { name: "Confirm" }),
    );
    await waitFor(() =>
      expect(canvasElement.querySelector("li code")).toBeInTheDocument(),
    );
    await expect(confirmations).toEqual([{ code: "123456" }]);
  },
};
export const Subscription: Story = {
  parameters: hostedScenario(hostedMember, subscription()),
};
export const BillingUnavailable: Story = {
  parameters: hostedScenario(
    hostedMember,
    subscription({ billingEnabled: false }),
  ),
};
export const CheckoutCancelled: Story = {
  ...Subscription,
  args: { billing: "cancelled" },
};
export const WaitingForPayment: Story = {
  ...Subscription,
  args: { billing: "returned" },
};
export const Complete: Story = {
  parameters: hostedScenario(
    hostedMember,
    subscription({ managementActive: true, status: "granted" }),
  ),
};
export const SignInRequired: Story = { parameters: scenario(guest) };
export const MissingWorkspace: Story = {
  args: { workspaceId: undefined },
  parameters: hostedScenario(hostedMember),
};
export const SelfHosted: Story = { parameters: scenario(hostedMember) };
export const ConnectionFailure: Story = {
  parameters: hostedScenario(
    hostedMember,
    http.get("/api/v1/workspaces/:workspaceId/subscription", () =>
      HttpResponse.error(),
    ),
  ),
};

let restartAttempts = 0;
export const RestartRequestFailure: Story = {
  beforeEach: () => {
    restartAttempts = 0;
    return () => {
      restartAttempts = 0;
    };
  },
  parameters: hostedScenario(
    unprotected,
    subscription(),
    http.post("/api/v1/auth/mfa/enroll", () =>
      ++restartAttempts === 1
        ? HttpResponse.json(enrollmentData)
        : HttpResponse.json(
            {
              code: "internal_error",
              message: "Workshop response.",
              requestId: "workshop-request",
            },
            { status: 500 },
          ),
    ),
  ),
  play: async (context) => {
    await ScanAuthenticator.play!(context);
    await waitFor(() =>
      expect(
        context.canvasElement.querySelector('button[name="restartSetup"]'),
      ).toBeEnabled(),
    );
    await userEvent.click(
      context.canvasElement.querySelector<HTMLButtonElement>(
        'button[name="restartSetup"]',
      )!,
    );
    await waitFor(() =>
      expect(
        within(context.canvasElement.ownerDocument.body).getByRole("dialog"),
      ).toBeVisible(),
    );
    await waitFor(() =>
      expect(
        within(within(context.canvasElement).getByRole("main")).getByRole(
          "heading",
          { level: 1 },
        ),
      ).toHaveFocus(),
    );
  },
};
