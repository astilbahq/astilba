---
title: LLMs.txt
description: Choose a generated text corpus for an agent that reads documentation without MCP.
prev: false
next: false
---

Astilba publishes build-generated text files for language models and other clients that can fetch documentation but cannot connect to an MCP server. Every file is derived from the same public pages as this site.

## Choose a file

| File | Use it when |
| --- | --- |
| [`/docs/llms.txt`](https://astilba.com/docs/llms.txt) | The client needs a small index that links to the available documentation sets. |
| [`/docs/llms-small.txt`](https://astilba.com/docs/llms-small.txt) | Context is limited and an abridged copy of the documentation is sufficient. |
| [`/docs/llms-full.txt`](https://astilba.com/docs/llms-full.txt) | The client can accept the complete published documentation in one response. |
| [`/docs/_llms-txt/astilba-env.txt`](https://astilba.com/docs/_llms-txt/astilba-env.txt) | The task concerns only Astilba Env and does not need site-wide material. |
| [`/docs/_llms-txt/astilba-cache.txt`](https://astilba.com/docs/_llms-txt/astilba-cache.txt) | The task concerns only Astilba Cache and does not need site-wide material. |

The generated files are snapshots of the current deployment. Fetch them again when current behavior or release status matters.

## Access one page

Every published documentation page is also available as Markdown. Replace the trailing slash in a documentation URL with `.md`. The links below are common entry points rather than a complete index; each product text file contains every catalogued page for that product.

- [`/docs/env/overview.md`](https://astilba.com/docs/env/overview.md); Env overview and public-alpha context.
- [`/docs/env/quickstart.md`](https://astilba.com/docs/env/quickstart.md); first server-side Env integration.
- [`/docs/env/browser-delivery.md`](https://astilba.com/docs/env/browser-delivery.md); validated JSON bootstrap and browser import boundary.
- [`/docs/env/declaration-reference.md`](https://astilba.com/docs/env/declaration-reference.md); declaration builders, codecs, consumers, targets, and rules.
- [`/docs/env/cli-reference.md`](https://astilba.com/docs/env/cli-reference.md); generation, target checking, planning, JSON formats, and exit statuses.
- [`/docs/env/migrate-from-next-dynamic-env.md`](https://astilba.com/docs/env/migrate-from-next-dynamic-env.md); migration guidance for Next.js applications.
- [`/docs/env/release-and-support.md`](https://astilba.com/docs/env/release-and-support.md); package, runtime, export, and public-alpha support boundaries.
- [`/docs/cache/overview.md`](https://astilba.com/docs/cache/overview.md); Cache overview and preview context.
- [`/docs/cache/api-reference.md`](https://astilba.com/docs/cache/api-reference.md); Cache API reference.
- [`/docs/agents/mcp.md`](https://astilba.com/docs/agents/mcp.md); MCP Server connection and usage guide.

Clients that control request headers can instead request the ordinary page URL with `Accept: text/markdown`. Prefer an individual page when the task is narrow; it consumes less context than a combined documentation set.

## Choose static text or MCP

Use an LLMs.txt file when a client works best with one ordinary HTTP response or cannot configure remote tools. Use the [MCP Server](https://astilba.com/docs/agents/mcp/) at `https://astilba.com/docs/mcp` when the client supports Streamable HTTP and benefits from targeted search, resource discovery, and bounded page reads.

Give a compatible coding agent this instruction to connect the public documentation skill and MCP endpoint:

```text
Fetch https://astilba.com/docs/agent-setup/prompt.md and follow its instructions.
```

Neither approach installs an Astilba package or grants access to an Astilba account.
