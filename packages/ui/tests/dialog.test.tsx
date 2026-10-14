import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { describe, expect, it, vi } from "vitest";

import { Dialog } from "../src/dialog";

interface DialogFixtureProps {
  readonly onOpenChange?: (open: boolean) => void;
}

const DialogFixture = ({ onOpenChange }: DialogFixtureProps) => (
  <main>
    <Dialog.Root onOpenChange={onOpenChange}>
      <Dialog.Trigger>Review release</Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Backdrop className="consumer-backdrop" />
        <Dialog.Viewport>
          <Dialog.Popup className="consumer-popup">
            <Dialog.Title>Release review</Dialog.Title>
            <Dialog.Description>
              The artifact was verified before publication.
            </Dialog.Description>
            <Dialog.Close>Done</Dialog.Close>
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  </main>
);

describe("Dialog", () => {
  it("labels the popup from its title and description", async () => {
    const user = userEvent.setup();
    render(<DialogFixture />);

    const trigger = screen.getByRole("button", { name: "Review release" });
    expect(trigger.getAttribute("type")).toBe("button");
    expect(trigger.getAttribute("aria-haspopup")).toBe("dialog");

    await user.click(trigger);
    const dialog = await screen.findByRole("dialog", {
      name: "Release review",
    });

    expect(dialog.getAttribute("aria-describedby")).toBe(
      screen.getByText("The artifact was verified before publication.").id
    );
  });

  it("applies the shared classes alongside consumer classes", async () => {
    const user = userEvent.setup();
    render(<DialogFixture />);

    await user.click(screen.getByRole("button", { name: "Review release" }));
    const dialog = await screen.findByRole("dialog");

    expect(dialog.classList.contains("astilba-dialog-popup")).toBe(true);
    expect(dialog.classList.contains("consumer-popup")).toBe(true);
    expect(
      dialog.parentElement?.classList.contains("astilba-dialog-viewport")
    ).toBe(true);
    expect(
      document.querySelector(".astilba-dialog-backdrop.consumer-backdrop")
    ).not.toBeNull();
    expect(
      screen
        .getByRole("heading", { name: "Release review" })
        .classList.contains("astilba-dialog-title")
    ).toBe(true);
    expect(
      screen
        .getByText("The artifact was verified before publication.")
        .classList.contains("astilba-dialog-description")
    ).toBe(true);
  });

  it("closes from the close control and Escape, returning focus to the trigger", async () => {
    const onOpenChange = vi.fn();
    const user = userEvent.setup();
    render(<DialogFixture onOpenChange={onOpenChange} />);

    const trigger = screen.getByRole("button", { name: "Review release" });
    await user.click(trigger);
    await user.click(await screen.findByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));

    await user.click(trigger);
    await screen.findByRole("dialog");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    expect(onOpenChange.mock.calls.map(([open]) => open)).toEqual([
      true,
      false,
      true,
      false,
    ]);
  });

  it("opens detached triggers with their payload", async () => {
    const handle = Dialog.createHandle<{ label: string }>();
    const user = userEvent.setup();
    render(
      <>
        <Dialog.Trigger handle={handle} payload={{ label: "Request flow" }}>
          Open request flow
        </Dialog.Trigger>
        <Dialog.Root handle={handle}>
          {({ payload }) => (
            <Dialog.Portal>
              <Dialog.Viewport>
                <Dialog.Popup>
                  <Dialog.Title>{payload?.label}</Dialog.Title>
                </Dialog.Popup>
              </Dialog.Viewport>
            </Dialog.Portal>
          )}
        </Dialog.Root>
      </>
    );

    await user.click(screen.getByRole("button", { name: "Open request flow" }));

    expect(
      await screen.findByRole("dialog", { name: "Request flow" })
    ).toBeDefined();
  });

  it("has no automatically detectable accessibility violations while open", async () => {
    const user = userEvent.setup();
    render(<DialogFixture />);

    await user.click(screen.getByRole("button", { name: "Review release" }));
    await screen.findByRole("dialog");
    const results = await axe.run(document.body, {
      rules: {
        "color-contrast": { enabled: false },
      },
    });

    expect(results.violations).toEqual([]);
  });
});
