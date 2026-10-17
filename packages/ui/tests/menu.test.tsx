import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { describe, expect, it, vi } from "vitest";

import { Menu } from "../src/menu";
import type { MenuLinkItemProps } from "../src/menu";

const validMenuLinkItemProperties = {
  href: "/docs",
  label: "Read the docs",
} satisfies MenuLinkItemProps;

// @ts-expect-error A menu link must have a native destination.
const invalidMenuLinkItemProperties: MenuLinkItemProps = {
  label: "Read the docs",
};

void validMenuLinkItemProperties;
void invalidMenuLinkItemProperties;

interface MenuFixtureProps {
  readonly onAction?: () => void;
}

const MenuFixture = ({ onAction }: MenuFixtureProps) => (
  <main>
    <Menu.Root>
      <Menu.Trigger>More actions</Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner>
          <Menu.Popup>
            <Menu.Item label="Run action" onClick={onAction}>
              <Menu.ItemLabel>Run action</Menu.ItemLabel>
            </Menu.Item>
            <Menu.Item disabled label="Unavailable">
              <Menu.ItemLabel>Unavailable</Menu.ItemLabel>
            </Menu.Item>
            <Menu.Item closeOnClick={false} label="Keep open">
              <Menu.ItemLabel>Keep open</Menu.ItemLabel>
            </Menu.Item>
            <Menu.LinkItem
              href="#docs"
              label="Read the docs"
              rel="noopener"
              target="_blank"
            >
              <Menu.ItemLabel>Read the docs</Menu.ItemLabel>
              <Menu.ItemTrailing aria-hidden="true">↗</Menu.ItemTrailing>
            </Menu.LinkItem>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  </main>
);

describe("Menu", () => {
  it("uses native trigger and link semantics", async () => {
    render(<MenuFixture />);

    const trigger = screen.getByRole("button", { name: "More actions" });
    expect(trigger.getAttribute("type")).toBe("button");
    expect(trigger.getAttribute("aria-haspopup")).toBe("menu");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(trigger);
    await waitFor(() =>
      expect(trigger.getAttribute("aria-expanded")).toBe("true")
    );

    const link = await screen.findByRole("menuitem", {
      name: "Read the docs",
    });
    expect(link.tagName).toBe("A");
    expect(link.getAttribute("href")).toBe("#docs");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener");
  });

  it("closes ordinary actions and keeps persistent actions open", async () => {
    const onAction = vi.fn();
    const user = userEvent.setup();
    render(<MenuFixture onAction={onAction} />);

    const trigger = screen.getByRole("button", { name: "More actions" });
    trigger.focus();
    await user.keyboard("{ArrowDown}");
    await user.click(screen.getByRole("menuitem", { name: "Keep open" }));
    expect(screen.getByRole("menu")).toBeDefined();

    await user.click(screen.getByRole("menuitem", { name: "Run action" }));
    expect(onAction).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("closes link items by default", async () => {
    const user = userEvent.setup();
    render(<MenuFixture />);

    const trigger = screen.getByRole("button", { name: "More actions" });
    trigger.focus();
    await user.keyboard("{ArrowDown}");
    await user.click(screen.getByRole("menuitem", { name: "Read the docs" }));

    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("traverses disabled items without activating them and restores focus after Escape", async () => {
    const user = userEvent.setup();
    render(<MenuFixture />);

    const trigger = screen.getByRole("button", { name: "More actions" });
    trigger.focus();
    await user.keyboard("{ArrowDown}");

    const runAction = await screen.findByRole("menuitem", {
      name: "Run action",
    });
    // Base UI highlights the first item before it moves focus into the menu;
    // a key pressed in between still goes to the trigger.
    await waitFor(() => expect(document.activeElement).toBe(runAction));
    expect(runAction.dataset.highlighted).toBeDefined();

    await user.keyboard("{ArrowDown}");
    const unavailable = screen.getByRole("menuitem", {
      name: "Unavailable",
    });
    expect(unavailable.dataset.highlighted).toBeDefined();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("menu")).toBeDefined();

    await user.keyboard("{ArrowDown}");
    expect(
      screen.getByRole("menuitem", { name: "Keep open" }).dataset.highlighted
    ).toBeDefined();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("does not open from a disabled trigger", async () => {
    const user = userEvent.setup();
    render(
      <Menu.Root>
        <Menu.Trigger disabled>More actions</Menu.Trigger>
        <Menu.Portal>
          <Menu.Positioner>
            <Menu.Popup>
              <Menu.Item label="Run action">Run action</Menu.Item>
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>
    );

    await user.click(screen.getByRole("button", { name: "More actions" }));
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("has no automatically detectable accessibility violations when open", async () => {
    const user = userEvent.setup();
    render(<MenuFixture />);

    await user.click(screen.getByRole("button", { name: "More actions" }));
    await screen.findByRole("menu");

    const results = await axe.run(document.body, {
      rules: {
        "color-contrast": { enabled: false },
        region: { enabled: false },
      },
    });

    expect(results.violations).toEqual([]);
  });
});
