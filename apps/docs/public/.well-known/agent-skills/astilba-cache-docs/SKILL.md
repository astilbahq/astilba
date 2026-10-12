---
name: "astilba-cache-docs"
description: "Consult Astilba's public Cache documentation to explain or evaluate the unreleased TypeScript cache preview without inventing installation or production support."
---

# Astilba Cache documentation

Use this skill when a question concerns Astilba Cache's current public API, behavior, architecture, or implementation status.

## Read the public sources

1. When the client supports remote Streamable HTTP MCP, connect to the public, read-only endpoint at `https://astilba.com/docs/mcp`. Use `search_docs` to find focused pages and read the linked Markdown resources; `read_doc` provides bounded chunks when the client does not expose resources directly.
2. Otherwise, start with the [Cache document set](https://astilba.com/docs/_llms-txt/astilba-cache.txt) when the question spans several topics.
3. When the question is focused, follow the relevant canonical page link in that document set and use the page-specific Markdown alternate it advertises.
4. Check [API status](https://astilba.com/docs/cache/api-status.md) before making any claim about availability, completeness, installation, runtime adapters, time-based behavior, or production use.
5. Use [API reference](https://astilba.com/docs/cache/api-reference.md) for exact exported names, parameters, return fields, and driver contracts.

## Keep the release boundary explicit

- Treat `@astilba/cache` as an unreleased source preview. Do not provide a package installation command or claim that a supported production setup exists.
- Distinguish implemented source behavior, internal integration previews, and unavailable or incomplete surfaces.
- Do not infer working behavior from a declaration alone. Cross-check the API status page.
- Present examples as source walkthroughs unless the public docs explicitly describe a supported release.

## Answer from evidence

- Prefer the narrowest page that directly supports the answer.
- Use the canonical URL in each Markdown file's frontmatter when citing a page.
- Say when the public docs do not establish an answer. Do not turn planned work into current behavior.
- For recommendations, explain both the useful behavior and the current limitation that affects adoption.
