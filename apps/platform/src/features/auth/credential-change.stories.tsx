import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { http, HttpResponse } from "msw/http";
import { useEffect, useState } from "react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { client, result, type Schema, type Session } from "../../api/client";
import { useConfirmCredential } from "../../api/step-up";
import { useSessionRequest } from "../../api/use-session-request";
import { BusyButton, ErrorNotice, Form, useAction } from "../../ui";
import { member, scenario } from "../../workshop/fixtures";

// The identity confirmation dialog (step-up.tsx), sending a current credential with a change.
const authenticator: Session = { ...member, recentAuth: false };
const refusal = (code: string, status: number) =>
  HttpResponse.json(
    {
      code,
      message: "Workshop response.",
      requestId: "workshop-request",
    } satisfies Schema["Error"],
    { status },
  );
let confirmations: Schema["Reauthenticate"][] = [];
const page = (canvas: HTMLElement) => within(canvas.ownerDocument.body);
const dialog = (canvas: HTMLElement) => page(canvas).findByRole("alertdialog");

async function confirm(canvas: HTMLElement) {
  await userEvent.click(
    within(await dialog(canvas)).getByRole("button", { name: "Confirm" }),
  );
}

async function enterCode(canvas: HTMLElement, code = "123456") {
  const confirmation = await dialog(canvas);
  const first = within(confirmation).getAllByRole("textbox")[0];
  await waitFor(() => expect(first).toHaveFocus());
  await userEvent.keyboard(code);
}

const meta = {
  title: "Patterns/Credential change",
  component: DisableTwoFactor,
  ...credentialChange(),
} satisfies Meta<typeof DisableTwoFactor>;
export default meta;
type Story = StoryObj<typeof meta>;

// A change to how the person signs in sends their current credential with the change itself,
// from the same dialog, instead of confirming identity first.
let changes: Schema["CredentialConfirmation"][] = [];
function DisableTwoFactor() {
  const action = useAction();
  const confirmCredential = useConfirmCredential();
  const request = useSessionRequest();
  const [done, setDone] = useState(false);
  return (
    <main id="main">
      <Form
        action={action}
        label="Two-factor authentication"
        onSubmit={() => {
          setDone(false);
          action.run(() =>
            request(
              (signal) =>
                confirmCredential(
                  (current, signal) =>
                    result(
                      client.POST("/api/v1/auth/mfa/disable", {
                        signal,
                        body: { current },
                      }),
                    ),
                  { signal },
                ),
              (session) => {
                if (session) setDone(true);
              },
            ),
          );
        }}
      >
        <ErrorNotice error={action.error} />
        {done && <p role="status">Two-factor authentication disabled.</p>}
        <BusyButton busy={action.isPending}>Disable two-factor</BusyButton>
      </Form>
    </main>
  );
}

// `answers` respond to each change in turn; any change after them goes through.
function credentialChange(...answers: (() => Response)[]) {
  return {
    render: () => <DisableTwoFactor />,
    parameters: scenario(
      authenticator,
      http.post("/api/v1/auth/mfa/disable", async ({ request }) => {
        changes.push(
          (await request.json()) as Schema["CredentialConfirmation"],
        );
        const answer = answers[changes.length - 1];
        return answer ? answer() : HttpResponse.json(authenticator);
      }),
      http.post("/api/v1/auth/reauthenticate", async ({ request }) => {
        confirmations.push((await request.json()) as Schema["Reauthenticate"]);
        return HttpResponse.json(authenticator);
      }),
    ),
    beforeEach: () => {
      changes = [];
      confirmations = [];
    },
  };
}

async function disableTwoFactor(canvas: HTMLElement) {
  await userEvent.click(
    within(canvas).getByRole("button", { name: "Disable two-factor" }),
  );
  return dialog(canvas);
}

async function closedWithFocusReturned(canvas: HTMLElement) {
  await waitFor(() =>
    expect(page(canvas).queryByRole("alertdialog")).toBeNull(),
  );
  await waitFor(() =>
    expect(
      within(canvas).getByRole("button", { name: "Disable two-factor" }),
    ).toHaveFocus(),
  );
}

// The typed code goes with the change as `current`; nothing is confirmed separately.
export const CredentialChange: Story = {
  ...credentialChange(),
  play: async ({ canvasElement }) => {
    const confirmation = await disableTwoFactor(canvasElement);
    await expect(confirmation).toHaveAccessibleName("Confirm your identity");
    await expect(confirmation).toHaveAccessibleDescription(
      /^Changing how you sign in needs your current credential\. .*authenticator app/,
    );
    await enterCode(canvasElement);
    await confirm(canvasElement);
    await expect(
      await within(canvasElement).findByRole("status"),
    ).toHaveTextContent("Two-factor authentication disabled.");
    await closedWithFocusReturned(canvasElement);
    await expect(changes).toEqual([{ current: { code: "123456" } }]);
    await expect(confirmations).toEqual([]);
  },
};

// Too many attempts, or no capacity to check the credential, stay in the dialog to try again.
export const CredentialChangeRetries: Story = {
  ...credentialChange(
    () => refusal("rate_limited", 429),
    () => refusal("temporarily_busy", 503),
  ),
  play: async ({ canvasElement }) => {
    await disableTwoFactor(canvasElement);
    await enterCode(canvasElement);
    await confirm(canvasElement);
    const confirmation = await dialog(canvasElement);
    await expect(
      await within(confirmation).findByRole("alert"),
    ).toHaveTextContent("Too many attempts. Try again later.");
    await confirm(canvasElement);
    await waitFor(() =>
      expect(within(confirmation).getByRole("alert")).toHaveTextContent(
        /Too many sign-ins are in progress/,
      ),
    );
    await expect(within(canvasElement).queryByRole("alert")).toBeNull();
    await confirm(canvasElement);
    await expect(
      await within(canvasElement).findByRole("status"),
    ).toBeVisible();
    await closedWithFocusReturned(canvasElement);
    await expect(changes).toHaveLength(3);
  },
};

// Any other failure is the change's own: the dialog closes and the screen that asked shows it.
export const CredentialChangeFails: Story = {
  ...credentialChange(() => refusal("internal_error", 500)),
  play: async ({ canvasElement }) => {
    await disableTwoFactor(canvasElement);
    await enterCode(canvasElement);
    await confirm(canvasElement);
    await expect(
      await within(canvasElement).findByRole("alert"),
    ).toHaveTextContent("Workshop response.");
    await closedWithFocusReturned(canvasElement);
    await expect(changes).toHaveLength(1);
  },
};

// Escape declines: nothing is sent, and the screen shows nothing.
export const CredentialChangeDeclined: Story = {
  ...credentialChange(),
  play: async ({ canvasElement }) => {
    await disableTwoFactor(canvasElement);
    await userEvent.keyboard("{Escape}");
    await closedWithFocusReturned(canvasElement);
    await expect(within(canvasElement).queryByRole("alert")).toBeNull();
    await expect(within(canvasElement).queryByRole("status")).toBeNull();
    await expect(changes).toEqual([]);
    await expect(confirmations).toEqual([]);
  },
};

// The screen that asked goes away while the dialog is open: the change is withdrawn, unsent.
function LeavingChange() {
  const [here, setHere] = useState(true);
  useEffect(() => {
    const leave = () => setHere(false);
    window.addEventListener("workshop-leave", leave);
    return () => window.removeEventListener("workshop-leave", leave);
  }, []);
  return here ? <DisableTwoFactor /> : <main id="main">Another page</main>;
}

export const CredentialChangeWithdrawn: Story = {
  ...credentialChange(),
  render: () => <LeavingChange />,
  play: async ({ canvasElement }) => {
    await disableTwoFactor(canvasElement);
    window.dispatchEvent(new Event("workshop-leave"));
    await waitFor(() =>
      expect(page(canvasElement).queryByRole("alertdialog")).toBeNull(),
    );
    await expect(within(canvasElement).getByRole("main")).toHaveTextContent(
      "Another page",
    );
    await expect(changes).toEqual([]);
  },
};

// Leaving while the change is in flight does not cut it short, which could commit it without
// the session it renews: the dialog stays until the change finishes, then closes quietly.
let finishChange = () => {};
export const CredentialChangeWithdrawnInFlight: Story = {
  ...credentialChange(),
  parameters: scenario(
    authenticator,
    http.post("/api/v1/auth/mfa/disable", async ({ request }) => {
      changes.push((await request.json()) as Schema["CredentialConfirmation"]);
      await new Promise<void>((resolve) => {
        finishChange = resolve;
      });
      return HttpResponse.json(authenticator);
    }),
  ),
  render: () => <LeavingChange />,
  play: async ({ canvasElement }) => {
    await disableTwoFactor(canvasElement);
    await enterCode(canvasElement);
    await confirm(canvasElement);
    await waitFor(() => expect(changes).toHaveLength(1));
    window.dispatchEvent(new Event("workshop-leave"));
    // The page behind the modal dialog is hidden from roles, so find it with hidden ones too.
    await within(canvasElement).findByText("Another page");
    const open = await dialog(canvasElement);
    await new Promise((resolve) => setTimeout(resolve, 300));
    await expect(open).toBeVisible();
    await expect(open).not.toHaveAttribute("data-closed");
    finishChange();
    await waitFor(() =>
      expect(page(canvasElement).queryByRole("alertdialog")).toBeNull(),
    );
    await expect(changes).toHaveLength(1);
  },
};

// Leaving after a refusal, with nothing in flight, withdraws the change at once.
export const CredentialChangeWithdrawnAfterRefusal: Story = {
  ...credentialChange(() => refusal("invalid_credentials", 400)),
  render: () => <LeavingChange />,
  play: async ({ canvasElement }) => {
    await disableTwoFactor(canvasElement);
    await enterCode(canvasElement, "000000");
    await confirm(canvasElement);
    const refused = within(await dialog(canvasElement)).getAllByRole(
      "textbox",
    )[0];
    await waitFor(() =>
      expect(refused).toHaveAttribute("aria-invalid", "true"),
    );
    window.dispatchEvent(new Event("workshop-leave"));
    await waitFor(() =>
      expect(page(canvasElement).queryByRole("alertdialog")).toBeNull(),
    );
    await expect(within(canvasElement).getByRole("main")).toHaveTextContent(
      "Another page",
    );
    await expect(changes).toHaveLength(1);
  },
};
