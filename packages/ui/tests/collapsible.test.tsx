import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { Collapsible } from "../src/collapsible";

const CollapsibleFixture = ({
  defaultOpen = false,
  disabled = false,
  onOpenChange,
}: {
  readonly defaultOpen?: boolean;
  readonly disabled?: boolean;
  readonly onOpenChange?: (open: boolean) => void;
}) => (
  <Collapsible.Root
    defaultOpen={defaultOpen}
    disabled={disabled}
    onOpenChange={onOpenChange}
  >
    <Collapsible.Trigger>Deployment details</Collapsible.Trigger>
    <Collapsible.Panel>
      <p>Deployed from a verified artifact.</p>
    </Collapsible.Panel>
  </Collapsible.Root>
);

const ControlledCollapsibleFixture = () => {
  const [open, setOpen] = useState(false);

  return (
    <Collapsible.Root onOpenChange={setOpen} open={open}>
      <Collapsible.Trigger>Release details</Collapsible.Trigger>
      <Collapsible.Panel>
        <p>Published with provenance.</p>
      </Collapsible.Panel>
    </Collapsible.Root>
  );
};

describe("Collapsible", () => {
  it("uses native disclosure semantics and reports uncontrolled changes", async () => {
    const onOpenChange = vi.fn();
    const user = userEvent.setup();
    render(<CollapsibleFixture onOpenChange={onOpenChange} />);

    const trigger = screen.getByRole("button", {
      name: "Deployment details",
    });
    expect(trigger.getAttribute("type")).toBe("button");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("Deployed from a verified artifact.")).toBeNull();

    await user.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(
      screen.getByText("Deployed from a verified artifact.")
    ).toBeDefined();

    await user.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(onOpenChange.mock.calls.map(([open]) => open)).toEqual([
      true,
      false,
    ]);
  });

  it("preserves controlled state updates", async () => {
    const user = userEvent.setup();
    render(<ControlledCollapsibleFixture />);

    const trigger = screen.getByRole("button", { name: "Release details" });
    await user.click(trigger);

    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("Published with provenance.")).toBeDefined();
  });

  it("preserves a polymorphic list-item root and trigger-panel relationship", async () => {
    const user = userEvent.setup();
    render(
      <ul>
        <Collapsible.Root render={<li />}>
          <Collapsible.Trigger>Concepts</Collapsible.Trigger>
          <Collapsible.Panel className="consumer-panel">
            <a href="#concepts">Read concepts</a>
          </Collapsible.Panel>
        </Collapsible.Root>
      </ul>
    );

    const trigger = screen.getByRole("button", { name: "Concepts" });
    expect(trigger.closest("li")).not.toBeNull();

    await user.click(trigger);
    const panelId = trigger.getAttribute("aria-controls");
    const panel = panelId ? document.querySelector(`#${panelId}`) : null;
    expect(panel).not.toBeNull();
    expect(panel?.classList.contains("astilba-collapsible-panel")).toBe(true);
    expect(panel?.classList.contains("consumer-panel")).toBe(true);
  });

  it("does not toggle while disabled", async () => {
    const user = userEvent.setup();
    render(<CollapsibleFixture disabled />);

    const trigger = screen.getByRole("button", {
      name: "Deployment details",
    });
    expect((trigger as HTMLButtonElement).disabled).toBe(true);
    await user.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("Deployed from a verified artifact.")).toBeNull();
  });

  it("passes through persistent and find-in-page panel behavior", () => {
    const { rerender } = render(
      <Collapsible.Root>
        <Collapsible.Trigger>Persistent details</Collapsible.Trigger>
        <Collapsible.Panel keepMounted>
          Retained while closed.
        </Collapsible.Panel>
      </Collapsible.Root>
    );

    expect(screen.getByText("Retained while closed.")).toBeDefined();
    expect(
      screen.getByText("Retained while closed.").closest("[hidden]")
    ).not.toBeNull();

    rerender(
      <Collapsible.Root>
        <Collapsible.Trigger>Findable details</Collapsible.Trigger>
        <Collapsible.Panel hiddenUntilFound>
          Findable while closed.
        </Collapsible.Panel>
      </Collapsible.Root>
    );

    expect(
      screen
        .getByText("Findable while closed.")
        .closest('[hidden="until-found"]')
    ).not.toBeNull();
  });

  it("has no automatically detectable accessibility violations while open", async () => {
    const { container } = render(<CollapsibleFixture defaultOpen />);
    const results = await axe.run(container, {
      rules: {
        "color-contrast": { enabled: false },
      },
    });

    expect(results.violations).toEqual([]);
  });
});
