import type { DocsIcon } from "./types";
import { withDocsBase } from "./urls.ts";

interface SiteDocsNavigation {
  group: string;
  icon: DocsIcon;
  label: string;
}

export interface SiteDocsPage {
  canonicalPath: string;
  id: string;
  markdownPath: string;
  navigation?: SiteDocsNavigation;
  presentation?: "product-home";
  productId?: string;
  sourcePath: string;
}

export const siteDocsPages: readonly SiteDocsPage[] = Object.freeze([
  {
    canonicalPath: withDocsBase("/"),
    id: "index",
    markdownPath: withDocsBase("/index.md"),
    sourcePath: "src/content/docs/index.md",
  },
  {
    canonicalPath: withDocsBase("/env/"),
    id: "env",
    markdownPath: withDocsBase("/env.md"),
    presentation: "product-home",
    productId: "env",
    sourcePath: "src/content/docs/env.md",
  },
  {
    canonicalPath: withDocsBase("/cache/"),
    id: "cache",
    markdownPath: withDocsBase("/cache.md"),
    presentation: "product-home",
    productId: "cache",
    sourcePath: "src/content/docs/cache.md",
  },
  {
    canonicalPath: withDocsBase("/agents/llms-txt/"),
    id: "agents/llms-txt",
    markdownPath: withDocsBase("/agents/llms-txt.md"),
    navigation: {
      group: "AI for Agents",
      icon: "file-digit",
      label: "LLMs.txt",
    },
    sourcePath: "src/content/docs/agents/llms-txt.md",
  },
  {
    canonicalPath: withDocsBase("/agents/mcp/"),
    id: "agents/mcp",
    markdownPath: withDocsBase("/agents/mcp.md"),
    navigation: {
      group: "AI for Agents",
      icon: "model-context-protocol",
      label: "MCP Server",
    },
    sourcePath: "src/content/docs/agents/mcp.md",
  },
]);

export const findSiteDocsPage = (id: string): SiteDocsPage | undefined =>
  siteDocsPages.find((page) => page.id === id);
