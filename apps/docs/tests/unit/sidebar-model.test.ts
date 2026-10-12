import { describe, expect, it } from "vitest";

import {
  collectDocsSidebarGroupIds,
  createDocsSidebarContext,
  createDocsSidebarEntries,
  createSiteSidebarEntries,
  getDocsSidebarHash,
} from "../../src/docs/sidebar-model";

type SidebarInput = Parameters<typeof createDocsSidebarEntries>[0];

const sidebarInput = [
  {
    type: "group",
    label: "Start",
    collapsed: false,
    badge: undefined,
    entries: [
      {
        type: "link",
        label: "Overview",
        href: "/docs/cache/overview/",
        isCurrent: true,
        badge: undefined,
        attrs: {
          class: "current",
          href: "/unsafe/",
          onclick: "unsafe()",
          style: "color: red",
          "data-nav-icon": "open-book",
          "data-product": "cache",
          hidden: false,
        },
      },
    ],
  },
] as SidebarInput;

describe("documentation sidebar model", () => {
  it("creates stable ids and filters reserved link attributes", () => {
    const entries = createDocsSidebarEntries(sidebarInput);
    const group = entries[0];

    expect(group?.type).toBe("group");
    if (!group || group.type !== "group") {
      throw new Error("Expected a sidebar group.");
    }

    const link = group.entries[0];
    expect(link?.type).toBe("link");
    if (!link || link.type !== "link") {
      throw new Error("Expected a sidebar link.");
    }

    expect(group.id).toBe("docs-group-0");
    expect(group.containsCurrent).toBe(true);
    expect(link.id).toBe("docs-link-0-0");
    expect(link.className).toBe("current");
    expect(link.icon).toBe("open-book");
    expect(link.attrs).toEqual({
      "data-nav-icon": "open-book",
      "data-product": "cache",
      hidden: false,
    });
  });

  it("collects groups recursively and hashes navigation identity", () => {
    const entries = createDocsSidebarEntries(sidebarInput);
    const hash = getDocsSidebarHash(entries);

    expect(collectDocsSidebarGroupIds(entries, () => true)).toEqual([
      "docs-group-0",
    ]);
    expect(getDocsSidebarHash(entries)).toBe(hash);
    expect(
      getDocsSidebarHash(
        createDocsSidebarEntries([
          ...sidebarInput,
          {
            type: "link",
            label: "Reference",
            href: "/docs/cache/api-reference/",
            isCurrent: false,
            badge: undefined,
            attrs: {},
          },
        ])
      )
    ).not.toBe(hash);
  });

  it("describes known and unknown documentation contexts", () => {
    const known = createDocsSidebarContext("/docs/cache/overview/");
    const unknown = createDocsSidebarContext("/outside/");

    expect(known.mode).toBe("product");
    expect(unknown.mode).toBe("catalog");
    if (known.mode !== "product" || unknown.mode !== "catalog") {
      throw new Error("Expected product and catalog sidebar contexts.");
    }

    expect(known.product.label).toBe("Cache");
    expect(known.product.href).toBe("/docs/cache/");
    expect(known.product.options).toMatchObject([
      {
        href: "/docs/env/",
        label: "Env",
        selected: false,
      },
      {
        href: "/docs/cache/",
        label: "Cache",
        selected: true,
      },
    ]);
    expect(known.version?.label).toBe("Unreleased");
    expect(unknown.products).toMatchObject([
      {
        label: "Env",
        href: "/docs/env/",
      },
      {
        label: "Cache",
        href: "/docs/cache/",
      },
    ]);

    const productHome = createDocsSidebarContext("/docs/cache/");
    expect(productHome.mode).toBe("product");
    if (productHome.mode !== "product") {
      throw new Error("Expected a product sidebar context.");
    }
    expect(productHome.product.href).toBe("/docs/cache/");
    expect(productHome.version?.label).toBe("Unreleased");

    const envPage = createDocsSidebarContext("/docs/env/overview/");
    expect(envPage.mode).toBe("product");
    if (envPage.mode !== "product") {
      throw new Error("Expected a product sidebar context.");
    }
    expect(envPage.product.label).toBe("Env");
    expect(envPage.version?.label).toBe("0.3");
    expect(envPage.version?.meta).toBe("Latest");
  });

  it("uses direct product links outside a product context", () => {
    const home = createDocsSidebarContext("/docs/");
    const agentPage = createDocsSidebarContext("/docs/agents/mcp/");

    expect(home).toMatchObject({
      mode: "catalog",
      products: [
        {
          ariaLabel: "Env documentation",
          href: "/docs/env/",
          label: "Env",
        },
        {
          ariaLabel: "Cache documentation",
          href: "/docs/cache/",
          label: "Cache",
        },
      ],
    });
    expect(agentPage).toEqual(home);
  });

  it("adds global agent navigation independently of product context", () => {
    const homeEntries = createSiteSidebarEntries("/docs/");
    const mcpEntries = createSiteSidebarEntries("/docs/agents/mcp/");
    const homeGroup = homeEntries[0];
    const mcpGroup = mcpEntries[0];

    expect(homeGroup).toMatchObject({
      type: "group",
      label: "AI for Agents",
      collapsed: false,
      containsCurrent: false,
    });
    expect(mcpGroup).toMatchObject({
      type: "group",
      label: "AI for Agents",
      containsCurrent: true,
    });
    expect(homeGroup?.type === "group" && homeGroup.entries).toMatchObject([
      {
        type: "link",
        label: "LLMs.txt",
        href: "/docs/agents/llms-txt/",
        icon: "file-digit",
        isCurrent: false,
      },
      {
        type: "link",
        label: "MCP Server",
        href: "/docs/agents/mcp/",
        icon: "model-context-protocol",
        isCurrent: false,
      },
    ]);
    expect(mcpGroup?.type === "group" && mcpGroup.entries[1]).toMatchObject({
      isCurrent: true,
    });
  });
});
