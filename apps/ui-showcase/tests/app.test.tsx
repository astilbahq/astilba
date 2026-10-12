import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import axe from "axe-core";
import { afterEach, describe, expect, it, vi } from "vitest";

import { App, stripSvgMetadata } from "../src/app";

describe("showcase", () => {
  afterEach(() => {
    cleanup();
    window.history.replaceState({}, "", "/");
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("has no automatically detectable accessibility violations", async () => {
    const { container } = render(<App />);

    const results = await axe.run(container, {
      rules: { "color-contrast": { enabled: false } },
    });

    expect(results.violations).toEqual([]);
  });

  it("changes theme without changing control semantics", () => {
    window.history.replaceState({}, "", "/icons/");
    render(<App />);

    const [themeToggle] = screen.getAllByRole("button", {
      name: "Use light theme",
    });
    if (!themeToggle) {
      throw new Error("Theme toggle not found");
    }
    fireEvent.click(themeToggle);

    expect(document.documentElement.dataset.theme).toBe("light");
    expect(
      screen.getAllByRole("button", { name: "Use dark theme" })
    ).toHaveLength(2);
  });

  it("shows copy feedback for the full reset interval", () => {
    vi.useFakeTimers();
    window.history.replaceState({}, "", "/icons/");
    render(<App />);

    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(screen.getByRole("tooltip", { name: "Copied!" })).toBeDefined();

    act(() => {
      vi.advanceTimersByTime(1399);
    });
    expect(screen.getByRole("tooltip", { name: "Copied!" })).toBeDefined();

    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(screen.queryByRole("tooltip", { name: "Copied!" })).toBeNull();
  });

  it("provides a focusable skip-link destination", () => {
    render(<App />);

    const skipLink = screen.getByRole("link", { name: "Skip to content" });
    const destination = document.querySelector("#showcase-content");

    expect(skipLink.getAttribute("href")).toBe("#showcase-content");
    expect(destination?.getAttribute("tabindex")).toBe("-1");
  });

  it("links the showcase brand back to Astilba", () => {
    render(<App />);

    expect(
      screen.getByRole("link", { name: "Astilba home" }).getAttribute("href")
    ).toBe("https://astilba.com/");
  });

  it("opens on brand and navigates between focused pages", () => {
    const pushState = vi.spyOn(window.history, "pushState");
    render(<App />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Astilba" })
    ).toBeDefined();
    const brandLink = screen.getByRole("link", { name: "Brand" });
    expect(brandLink.getAttribute("aria-current")).toBe("page");
    fireEvent.click(brandLink);
    expect(pushState).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("link", { name: "Actions" }));
    expect(pushState).toHaveBeenCalledOnce();

    expect(window.location.pathname).toBe("/actions/");
    expect(
      screen.getByRole("heading", { level: 1, name: "Actions" })
    ).toBeDefined();
    expect(
      screen.getByRole("link", { name: "Actions" }).getAttribute("aria-current")
    ).toBe("page");
  });

  it("parses SVG metadata and rejects malformed comment openers", () => {
    const svg =
      '<?xml version="1.0"?><!-- editor --><svg xmlns="http://www.w3.org/2000/svg"><!-- nested --><path d="M0 0" /></svg>';

    const sanitized = stripSvgMetadata(svg);

    expect(sanitized).toContain("<svg");
    expect(sanitized).toContain('<path d="M0 0"/>');
    expect(sanitized).not.toContain("<?xml");
    expect(sanitized).not.toContain("<!--");
    expect(() =>
      stripSvgMetadata(
        '<!-- <!-- editor --><svg xmlns="http://www.w3.org/2000/svg" />'
      )
    ).toThrow("The brand asset is not valid SVG.");
  });
});
