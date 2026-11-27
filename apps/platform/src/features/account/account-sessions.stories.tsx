import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, within } from "storybook/test";
import type { Schema, Session } from "../../api/client";
import { scenario } from "../../workshop/fixtures";
import { Account } from "./account";
import {
  accountPage,
  confirmationClosed,
  confirmWith,
  enabled,
  resetSent,
  sent,
  sessionMutation,
  staleEnabled,
} from "./account.workshop";

const meta = {
  title: "Pages/Account security/Signed-in sessions",
  component: Account,
  decorators: [accountPage],
  parameters: scenario(enabled),
  beforeEach: resetSent,
} satisfies Meta<typeof Account>;
export default meta;
type Story = StoryObj<typeof meta>;

// Signed-in sessions: this one, and two others the person can sign out.
const thisSession: Schema["AccountSession"] = {
  id: "00000000-0000-4000-8000-000000000010",
  current: true,
  signedInAt: "2026-11-20T09:30:00Z",
  lastSeenAt: "2026-11-26T12:00:00Z",
  browser: "firefox",
  os: "macos",
  network: "203.0.113.0/24",
};
const laptop: Schema["AccountSession"] = {
  id: "00000000-0000-4000-8000-000000000011",
  current: false,
  signedInAt: "2026-11-18T08:00:00Z",
  lastSeenAt: "2026-11-25T17:45:00Z",
  browser: "chrome",
  os: "windows",
  network: "198.51.100.0/24",
};
const phone: Schema["AccountSession"] = {
  id: "00000000-0000-4000-8000-000000000012",
  current: false,
  signedInAt: "2026-11-24T21:10:00Z",
  lastSeenAt: "2026-11-24T21:15:00Z",
  browser: "safari",
  os: "ios",
};
const signedIn = { sessions: [thisSession, laptop, phone], unlisted: 0 };
const alone = { sessions: [thisSession], unlisted: 0 };
// The list starts as initial, and each sign-out answers with after.
function sessionsScenario(
  initial: Schema["AccountSessions"],
  after: Schema["AccountSessions"] = initial,
  session: Session = enabled,
  ...actions: Parameters<typeof scenario>[1][]
) {
  let current = initial;
  const parameters = scenario(
    session,
    ...actions,
    http.delete("/api/v1/auth/sessions/:id", ({ params }) => {
      sent.push(params.id);
      current = after;
      return HttpResponse.json(after);
    }),
    http.post("/api/v1/auth/sessions/sign-out-others", () => {
      sent.push("others");
      current = after;
      return HttpResponse.json(after);
    }),
  );
  parameters.msw.handlers.session.unshift(
    http.get("/api/v1/auth/sessions", () => HttpResponse.json(current)),
  );
  return {
    parameters,
    beforeEach: () => {
      current = initial;
    },
  };
}
const sessionList = (canvas: HTMLElement) =>
  within(
    within(canvas)
      .getByRole("heading", { name: "Signed-in sessions" })
      .closest("section")!,
  );

// Each session says what it ran in, where from and when; only the others can be signed out.
export const SignedInSessions: Story = {
  ...sessionsScenario({ ...signedIn, unlisted: 2 }),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    const items = await list.findAllByRole("listitem");
    await expect(items).toHaveLength(3);
    await expect(items[0]).toHaveTextContent("Firefox on macOS");
    await expect(items[0]).toHaveTextContent("This session");
    await expect(items[0]).toHaveTextContent("Active now");
    await expect(
      within(items[0]).queryByRole("button", { name: "Sign out" }),
    ).toBeNull();
    await expect(items[1]).toHaveTextContent("Network 198.51.100.0/24");
    await expect(items[2]).toHaveTextContent("Network unknown");
    await expect(
      within(items[1]).getByRole("button", { name: "Sign out" }),
    ).toHaveAccessibleDescription(/^Chrome on Windows/);
    await expect(
      list.getByText(
        "2 older sessions aren’t shown. Signing out all other sessions ends them too.",
      ),
    ).toBeVisible();
  },
};
// Signing out one session ends it; focus moves to the section, since its button is gone.
export const SignOutASession: Story = {
  ...sessionsScenario(signedIn, {
    sessions: [thisSession, phone],
    unlisted: 0,
  }),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    const items = await list.findAllByRole("listitem");
    await userEvent.click(
      within(items[1]).getByRole("button", { name: "Sign out" }),
    );
    await expect(await list.findByText("Session signed out.")).toHaveAttribute(
      "role",
      "status",
    );
    await expect(list.getAllByRole("listitem")).toHaveLength(2);
    await expect(
      list.getByRole("heading", { name: "Signed-in sessions" }),
    ).toHaveFocus();
    await expect(sent).toEqual([laptop.id]);
  },
};
// Without recent authentication, signing out asks the person to confirm their identity first.
export const SignOutOtherSessionsConfirmsIdentity: Story = {
  ...(() => {
    const confirmed = sessionMutation(staleEnabled, enabled, "reauthenticate");
    const signingOut = sessionsScenario(signedIn, alone, staleEnabled);
    // Only its session read: its defaults would answer for the list this story sets.
    signingOut.parameters.msw.handlers.session.unshift(
      ...confirmed.parameters.msw.handlers.session.filter(
        (handler) => handler.info.path === "/api/v1/session",
      ),
    );
    signingOut.parameters.msw.handlers.action.unshift(
      ...confirmed.parameters.msw.handlers.action,
    );
    return {
      parameters: signingOut.parameters,
      beforeEach: () => {
        confirmed.beforeEach();
        signingOut.beforeEach();
      },
    };
  })(),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    await userEvent.click(
      await list.findByRole("button", { name: "Sign out all other sessions" }),
    );
    await confirmWith(canvasElement);
    await confirmationClosed(canvasElement);
    await expect(
      await list.findByText("All other sessions signed out."),
    ).toHaveAttribute("role", "status");
    await expect(
      list.getByText("No other sessions are signed in."),
    ).toBeVisible();
    await expect(sent).toEqual([{ code: "123456" }, "others"]);
  },
};
// A list that cannot be read says so and offers to try again.
export const SessionsUnavailable: Story = {
  parameters: (() => {
    const parameters = scenario(enabled);
    parameters.msw.handlers.session.unshift(
      http.get("/api/v1/auth/sessions", () =>
        HttpResponse.json(
          {
            code: "internal_error",
            message: "Workshop response.",
            requestId: "workshop-request",
          } satisfies Schema["Error"],
          { status: 500 },
        ),
      ),
    );
    return parameters;
  })(),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    await expect(
      await list.findByText("Your sessions could not be loaded."),
    ).toBeVisible();
    await expect(list.getByRole("button", { name: "Try again" })).toBeVisible();
  },
};
