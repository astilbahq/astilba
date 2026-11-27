import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
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
const failure = (code: Schema["Error"]["code"], status: number) =>
  HttpResponse.json(
    {
      code,
      message: "Workshop response.",
      requestId: "workshop-request",
    } satisfies Schema["Error"],
    { status },
  );
// The list starts as initial, and each sign-out answers with after. A sign-out can instead fail
// before ending anything, fail after ending what it was asked to ("lost"), or find a single
// session already signed out; the list becomes after unless it fails before ending anything.
function sessionsScenario(
  initial: Schema["AccountSessions"],
  {
    after = initial,
    session = enabled,
    signOut = "succeeds",
  }: {
    after?: Schema["AccountSessions"];
    session?: Session;
    signOut?: "succeeds" | "fails" | "lost" | "gone";
  } = {},
) {
  let current = initial;
  const answer = () => {
    if (signOut === "fails") return failure("internal_error", 500);
    current = after;
    if (signOut === "lost") return failure("internal_error", 502);
    return signOut === "gone"
      ? failure("not_found", 404)
      : HttpResponse.json(after);
  };
  const parameters = scenario(
    session,
    http.delete("/api/v1/auth/sessions/:id", ({ params }) => {
      sent.push(params.id);
      return answer();
    }),
    http.post("/api/v1/auth/sessions/sign-out-others", () => {
      sent.push("others");
      return answer();
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
    await expect(within(items[0]).queryByRole("button")).toBeNull();
    await expect(items[1]).toHaveTextContent("Network 198.51.100.0/24");
    await expect(items[2]).toHaveTextContent("Network unknown");
    // Each button names the session it ends, which lists of controls show without context.
    await expect(
      within(items[1]).getByRole("button", {
        name: "Sign out Chrome on Windows",
      }),
    ).toHaveAccessibleDescription(/^Chrome on Windows/);
    await expect(
      within(items[2]).getByRole("button", { name: "Sign out Safari on iOS" }),
    ).toBeVisible();
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
    after: { sessions: [thisSession, phone], unlisted: 0 },
  }),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    const items = await list.findAllByRole("listitem");
    await userEvent.click(
      within(items[1]).getByRole("button", {
        name: "Sign out Chrome on Windows",
      }),
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
// A sign-out that fails says so, keeps the list and returns focus to the button that asked.
export const SignOutFails: Story = {
  ...sessionsScenario(signedIn, { signOut: "fails" }),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    const button = within((await list.findAllByRole("listitem"))[1]).getByRole(
      "button",
      { name: "Sign out Chrome on Windows" },
    );
    await userEvent.click(button);
    await expect(await list.findByRole("alert")).toHaveTextContent(
      "The session could not be signed out.",
    );
    await expect(list.getAllByRole("listitem")).toHaveLength(3);
    await expect(button).toHaveFocus();
    await expect(sent).toEqual([laptop.id]);
  },
};
// A sign-out whose answer was lost after it ended the session is done, as reading the list again
// shows; focus moves to the section, since its button went with it.
export const SignOutAnswerLost: Story = {
  ...sessionsScenario(signedIn, {
    after: { sessions: [thisSession, phone], unlisted: 0 },
    signOut: "lost",
  }),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    await userEvent.click(
      within((await list.findAllByRole("listitem"))[1]).getByRole("button", {
        name: "Sign out Chrome on Windows",
      }),
    );
    await expect(await list.findByText("Session signed out.")).toHaveAttribute(
      "role",
      "status",
    );
    await expect(list.queryByRole("alert")).toBeNull();
    await expect(list.getAllByRole("listitem")).toHaveLength(2);
    await expect(
      list.getByRole("heading", { name: "Signed-in sessions" }),
    ).toHaveFocus();
  },
};
// Failing to sign out the rest says so for all of them, and keeps focus on their button.
export const SignOutOtherSessionsFails: Story = {
  ...sessionsScenario(signedIn, { signOut: "fails" }),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    const button = await list.findByRole("button", {
      name: "Sign out all other sessions",
    });
    await userEvent.click(button);
    await expect(await list.findByRole("alert")).toHaveTextContent(
      "The other sessions could not be signed out.",
    );
    await expect(list.getAllByRole("listitem")).toHaveLength(3);
    await expect(button).toHaveFocus();
    await expect(sent).toEqual(["others"]);
  },
};
// A session signed out elsewhere meanwhile is as good as signed out here.
export const SignOutASessionAlreadySignedOut: Story = {
  ...sessionsScenario(signedIn, {
    after: { sessions: [thisSession, phone], unlisted: 0 },
    signOut: "gone",
  }),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    const items = await list.findAllByRole("listitem");
    await userEvent.click(
      within(items[1]).getByRole("button", {
        name: "Sign out Chrome on Windows",
      }),
    );
    await expect(
      await list.findByText("That session was already signed out."),
    ).toHaveAttribute("role", "status");
    await expect(list.queryByRole("alert")).toBeNull();
    await waitFor(() => expect(list.getAllByRole("listitem")).toHaveLength(2));
    await expect(
      list.getByRole("heading", { name: "Signed-in sessions" }),
    ).toHaveFocus();
  },
};
// Without recent authentication, signing out asks the person to confirm their identity first.
export const SignOutOtherSessionsConfirmsIdentity: Story = {
  ...(() => {
    const confirmed = sessionMutation(staleEnabled, enabled, "reauthenticate");
    const signingOut = sessionsScenario(signedIn, {
      after: alone,
      session: staleEnabled,
    });
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
// A list that cannot be read says so, and trying again reads it.
export const SessionsUnavailable: Story = {
  ...(() => {
    let failed = false;
    const parameters = scenario(enabled);
    parameters.msw.handlers.session.unshift(
      http.get("/api/v1/auth/sessions", () => {
        if (failed) return HttpResponse.json(alone);
        failed = true;
        return failure("internal_error", 500);
      }),
    );
    return {
      parameters,
      beforeEach: () => {
        failed = false;
      },
    };
  })(),
  play: async ({ canvasElement }) => {
    const list = sessionList(canvasElement);
    await expect(
      await list.findByText("Your sessions could not be loaded."),
    ).toBeVisible();
    await userEvent.click(list.getByRole("button", { name: "Try again" }));
    // Its button goes away as the list is read again, so focus moves to the section.
    await expect(
      list.getByRole("heading", { name: "Signed-in sessions" }),
    ).toHaveFocus();
    await expect(await list.findAllByRole("listitem")).toHaveLength(1);
    await expect(list.queryByRole("alert")).toBeNull();
  },
};
