import type { Decorator } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { css } from "../../../styled-system/css";
import type { Schema, Session } from "../../api/client";
import { member, scenario } from "../../workshop/fixtures";

// What the account page's stories share: its sessions, the page around it, what the workshop
// server was sent and the identity confirmation.
export const enabled: Session = { ...member, recentAuth: true };
export const staleEnabled: Session = { ...enabled, recentAuth: false };

export const accountPage: Decorator = (Story) => (
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
);

// What the workshop server was sent, reset before each story.
export const sent: unknown[] = [];
export function resetSent() {
  sent.length = 0;
}
export function sessionMutation(
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

// Changes to how the person signs in ask for the current credential in this dialog.
export const confirmation = (canvas: HTMLElement) =>
  within(canvas.ownerDocument.body).findByRole("alertdialog", {
    name: "Confirm your identity",
  });
// Answers the dialog with the password, or with an authenticator code when given none.
export async function confirmWith(canvas: HTMLElement, password?: string) {
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
export const confirmationClosed = (canvas: HTMLElement) =>
  waitFor(() =>
    expect(
      within(canvas.ownerDocument.body).queryByRole("alertdialog", {
        name: "Confirm your identity",
      }),
    ).toBeNull(),
  );
