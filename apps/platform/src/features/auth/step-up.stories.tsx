import type { Meta, StoryObj } from "@storybook/tanstack-react";
import type { QueryClient } from "@tanstack/react-query";
import { http, HttpResponse } from "msw/http";
import { useEffect, useState } from "react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import {
  client,
  result,
  type Schema,
  type Session,
  setSession,
} from "../../api/client";
import { sessionOptions } from "../../api/session";
import { BusyButton, ErrorNotice, Form, useAction } from "../../ui";
import { member, scenario } from "../../workshop/fixtures";

const workspaceId = "00000000-0000-4000-8000-000000000002";
const authenticator: Session = { ...member, recentAuth: false };
const password: Session = {
  ...authenticator,
  user: { ...authenticator.user!, mfaEnabled: false },
};
const refusal = (code: string, status: number) =>
  HttpResponse.json(
    {
      code,
      message: "Workshop response.",
      requestId: "workshop-request",
    } satisfies Schema["Error"],
    { status },
  );

// What the workshop server saw, reset before each story.
let recent = false;
let saves = 0;
let confirmations: Schema["Reauthenticate"][] = [];

/**
 * A save that needs recent authentication, and the reauthentication endpoint. `confirm`
 * answers each confirmation; `renews` says whether a confirmation lets the save through.
 */
function stepUp(
  session: Session,
  confirm: (body: Schema["Reauthenticate"]) => Response = () =>
    HttpResponse.json({ ...session, recentAuth: true }),
  renews = true,
) {
  const parameters = scenario(
    session,
    http.put(`/api/v1/workspaces/${workspaceId}`, () => {
      saves++;
      return recent
        ? HttpResponse.json({
            id: workspaceId,
            name: "Example workspace",
            role: "owner",
            requireMfa: true,
          } satisfies Schema["Workspace"])
        : refusal("recent_auth_required", 403);
    }),
    http.post("/api/v1/auth/reauthenticate", async ({ request }) => {
      const body = (await request.json()) as Schema["Reauthenticate"];
      confirmations.push(body);
      const response = confirm(body);
      if (response.ok && renews) recent = true;
      return response;
    }),
  );
  parameters.msw.handlers.session.unshift(
    http.get("/api/v1/session", () =>
      HttpResponse.json({ ...session, recentAuth: recent }),
    ),
  );
  return {
    parameters,
    beforeEach: () => {
      recent = false;
      saves = 0;
      confirmations = [];
    },
  };
}

const saveSettings = () =>
  result(
    client.PUT("/api/v1/workspaces/{workspaceId}", {
      params: { path: { workspaceId } },
      body: { name: "Example workspace", requireMfa: true },
    }),
  );

function WorkspaceSettings() {
  const action = useAction();
  const [saved, setSaved] = useState(false);
  return (
    <main id="main">
      <Form
        action={action}
        label="Workspace settings"
        onSubmit={() => {
          setSaved(false);
          action.run(async () => {
            await saveSettings();
            setSaved(true);
          });
        }}
      >
        <ErrorNotice error={action.error} />
        {saved && <p role="status">Settings saved.</p>}
        <BusyButton busy={action.isPending}>Save settings</BusyButton>
      </Form>
    </main>
  );
}

const meta = {
  title: "Patterns/Identity confirmation",
  component: WorkspaceSettings,
  ...stepUp(authenticator),
} satisfies Meta<typeof WorkspaceSettings>;
export default meta;
type Story = StoryObj<typeof meta>;

const page = (canvas: HTMLElement) => within(canvas.ownerDocument.body);
const dialog = (canvas: HTMLElement) => page(canvas).findByRole("alertdialog");

async function save(canvas: HTMLElement) {
  await userEvent.click(
    within(canvas).getByRole("button", { name: "Save settings" }),
  );
  return dialog(canvas);
}

async function confirm(canvas: HTMLElement) {
  await userEvent.click(
    within(await dialog(canvas)).getByRole("button", { name: "Confirm" }),
  );
}

async function enterCode(canvas: HTMLElement, code = "123456") {
  const confirmation = await dialog(canvas);
  const first = within(confirmation).getAllByRole("textbox")[0];
  await waitFor(() => expect(first).toHaveFocus());
  // Each digit moves focus to the next box, as it does for a person typing.
  await userEvent.keyboard(code);
}

async function savedAfterRetry(canvas: HTMLElement) {
  await expect(await within(canvas).findByRole("status")).toHaveTextContent(
    "Settings saved.",
  );
  await waitFor(() =>
    expect(page(canvas).queryByRole("alertdialog")).toBeNull(),
  );
  // The refused save, then exactly one retry.
  await expect(saves).toBe(2);
  // Focus is back on the control that asked, once its retried save let it go.
  await waitFor(() =>
    expect(
      within(canvas).getByRole("button", { name: "Save settings" }),
    ).toHaveFocus(),
  );
}

// Two-factor accounts confirm with a fresh authenticator code alone, then the save retries.
export const Authenticator: Story = {
  play: async ({ canvasElement }) => {
    const confirmation = await save(canvasElement);
    await expect(confirmation).toHaveAccessibleName("Confirm your identity");
    await expect(confirmation).toHaveAccessibleDescription(/authenticator app/);
    await expect(
      within(confirmation).queryByLabelText("Password"),
    ).not.toBeInTheDocument();
    await enterCode(canvasElement);
    await confirm(canvasElement);
    await savedAfterRetry(canvasElement);
    await expect(confirmations).toEqual([{ code: "123456" }]);
  },
};

export const RecoveryCode: Story = {
  play: async ({ canvasElement }) => {
    const confirmation = await save(canvasElement);
    await userEvent.click(
      within(confirmation).getByRole("button", { name: "Use a recovery code" }),
    );
    const input = within(confirmation).getByLabelText("Recovery code");
    await waitFor(() => expect(input).toHaveFocus());
    await expect(input).toHaveAccessibleDescription(/signs out your other/);
    await userEvent.type(input, " EXAMPLE01RECOVERYCODE ");
    await confirm(canvasElement);
    await savedAfterRetry(canvasElement);
    await expect(confirmations).toEqual([{ code: "EXAMPLE01RECOVERYCODE" }]);
  },
};

// Accounts without two-factor authentication confirm with their password.
export const Password: Story = {
  ...stepUp(password),
  play: async ({ canvasElement }) => {
    const confirmation = await save(canvasElement);
    await expect(confirmation).toHaveAccessibleDescription(/your password/);
    const input = within(confirmation).getByLabelText("Password");
    await waitFor(() => expect(input).toHaveFocus());
    await expect(
      within(confirmation).queryByRole("button", {
        name: "Use a recovery code",
      }),
    ).not.toBeInTheDocument();
    await userEvent.type(input, "correct horse battery staple");
    await userEvent.keyboard("{Enter}");
    await savedAfterRetry(canvasElement);
    await expect(confirmations).toEqual([
      { password: "correct horse battery staple" },
    ]);
  },
};

// A wrong code is explained on the field, keeps the dialog open and retries nothing.
export const WrongCode: Story = {
  ...stepUp(authenticator, () => refusal("invalid_credentials", 400)),
  play: async ({ canvasElement }) => {
    await save(canvasElement);
    await enterCode(canvasElement, "000000");
    await confirm(canvasElement);
    const confirmation = await dialog(canvasElement);
    const first = within(confirmation).getAllByRole("textbox")[0];
    await waitFor(() =>
      expect(
        within(confirmation).getByRole("group"),
      ).toHaveAccessibleDescription(/invalid or has already been used/),
    );
    await waitFor(() => expect(first).toHaveFocus());
    await expect(first).toHaveAttribute("aria-invalid", "true");
    await expect(saves).toBe(1);
  },
};

export const WrongPassword: Story = {
  ...stepUp(password, () => refusal("invalid_credentials", 400)),
  play: async ({ canvasElement }) => {
    const confirmation = await save(canvasElement);
    await userEvent.type(
      within(confirmation).getByLabelText("Password"),
      "not the password",
    );
    await confirm(canvasElement);
    await waitFor(() =>
      expect(
        within(confirmation).getByLabelText("Password"),
      ).toHaveAccessibleDescription("This password is incorrect. Try again."),
    );
    await expect(saves).toBe(1);
  },
};

// Spent attempts say so in the dialog, with the way on that needs no attempts.
export const Lockout: Story = {
  ...stepUp(authenticator, () => refusal("rate_limited", 429)),
  play: async ({ canvasElement }) => {
    await save(canvasElement);
    await enterCode(canvasElement);
    await confirm(canvasElement);
    const confirmation = await dialog(canvasElement);
    await expect(
      await within(confirmation).findByRole("alert"),
    ).toHaveTextContent(/Too many attempts.*sign out and sign in again/);
    await expect(saves).toBe(1);
  },
};

// Escape declines: the dialog closes, focus returns to the control and nothing is shown.
export const Declined: Story = {
  play: async ({ canvasElement }) => {
    const button = within(canvasElement).getByRole("button", {
      name: "Save settings",
    });
    await save(canvasElement);
    await userEvent.keyboard("{Escape}");
    await waitFor(() =>
      expect(page(canvasElement).queryByRole("alertdialog")).toBeNull(),
    );
    await waitFor(() => expect(button).toHaveFocus());
    await expect(within(canvasElement).queryByRole("alert")).toBeNull();
    await expect(saves).toBe(1);
    await expect(confirmations).toEqual([]);
  },
};

// A save refused again after a confirmation is not retried a second time.
export const RetriesOnce: Story = {
  ...stepUp(authenticator, undefined, false),
  play: async ({ canvasElement }) => {
    await save(canvasElement);
    await enterCode(canvasElement);
    await confirm(canvasElement);
    await expect(
      await within(canvasElement).findByRole("alert"),
    ).toHaveTextContent("Workshop response.");
    await waitFor(() =>
      expect(page(canvasElement).queryByRole("alertdialog")).toBeNull(),
    );
    await expect(saves).toBe(2);
  },
};

// Tab stays in the dialog, in reading order, and wraps from its last control.
export const KeyboardOrder: Story = {
  ...stepUp(password),
  play: async ({ canvasElement }) => {
    const confirmation = await save(canvasElement);
    const input = within(confirmation).getByLabelText("Password");
    await waitFor(() => expect(input).toHaveFocus());
    const order = [
      within(confirmation).getByRole("button", { name: "Show password" }),
      within(confirmation).getByRole("button", { name: "Cancel" }),
      within(confirmation).getByRole("button", { name: "Confirm" }),
    ];
    for (const control of order) {
      await userEvent.tab();
      await waitFor(() => expect(control).toHaveFocus());
    }
    await userEvent.tab();
    await waitFor(() => expect(input).toHaveFocus());
    await userEvent.tab({ shift: true });
    await waitFor(() => expect(order.at(-1)).toHaveFocus());
  },
};

// The screen that asked goes away while the confirmation is open, as after browser Back.
function LeavingScreen() {
  const [here, setHere] = useState(true);
  useEffect(() => {
    const leave = () => setHere(false);
    window.addEventListener("workshop-leave", leave);
    return () => window.removeEventListener("workshop-leave", leave);
  }, []);
  return here ? <WorkspaceSettings /> : <main id="main">Another page</main>;
}

// Leaving withdraws the confirmation, and the save is never sent again from the old screen.
export const ScreenLeftWhileConfirming: Story = {
  render: () => <LeavingScreen />,
  play: async ({ canvasElement }) => {
    await save(canvasElement);
    window.dispatchEvent(new Event("workshop-leave"));
    await waitFor(() =>
      expect(page(canvasElement).queryByRole("alertdialog")).toBeNull(),
    );
    await expect(within(canvasElement).getByRole("main")).toHaveTextContent(
      "Another page",
    );
    await expect(saves).toBe(1);
    await expect(confirmations).toEqual([]);
  },
};

function TwoSaves() {
  const first = useAction();
  const second = useAction();
  const [saved, setSaved] = useState(0);
  return (
    <main id="main">
      {saved > 0 && <p role="status">{saved} saved.</p>}
      <button
        type="button"
        onClick={() => {
          for (const action of [first, second])
            action.run(async () => {
              await saveSettings();
              setSaved((count) => count + 1);
            });
        }}
      >
        Save both
      </button>
    </main>
  );
}

// Two refused actions share one confirmation, and each runs again once.
export const ConcurrentActionsShareOnePrompt: Story = {
  render: () => <TwoSaves />,
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole("button", { name: "Save both" }),
    );
    await waitFor(() => expect(saves).toBe(2));
    await enterCode(canvasElement);
    await confirm(canvasElement);
    await expect(
      await within(canvasElement).findByText("2 saved."),
    ).toBeVisible();
    await waitFor(() =>
      expect(page(canvasElement).queryByRole("alertdialog")).toBeNull(),
    );
    await expect(saves).toBe(4);
    await expect(confirmations).toEqual([{ code: "123456" }]);
  },
};

// Another account signing in while the confirmation is open declines it: the waiting save
// belonged to the previous account and is not sent again.
export const AccountChangedWhileConfirming: Story = {
  play: async (context) => {
    await save(context.canvasElement);
    const queries = context.loaded.workshop.queryClient as QueryClient;
    queries.setQueryData(
      sessionOptions.queryKey,
      setSession({
        ...authenticator,
        user: {
          ...authenticator.user!,
          id: "00000000-0000-4000-8000-000000000009",
          email: "another@example.test",
        },
      }),
    );
    await waitFor(() =>
      expect(page(context.canvasElement).queryByRole("alertdialog")).toBeNull(),
    );
    await expect(saves).toBe(1);
    await expect(confirmations).toEqual([]);
  },
};
