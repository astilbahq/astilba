---
title: MCP Server
description: Connect an MCP client to search and read Astilba's published documentation.
prev: false
next: false
---

Astilba provides a public, read-only Model Context Protocol endpoint for the same Markdown corpus published on this site.

Configure a remote Streamable HTTP server with this URL:

```text
https://astilba.com/docs/mcp
```

The endpoint does not require credentials or create a session. It cannot change documentation, access accounts, follow arbitrary URLs, or read private Astilba material.

Add it to an MCP client as a remote server. Client configuration formats differ, so use the endpoint above rather than copying configuration intended for another client.

For a compatible coding agent, the shortest setup path is:

```text
Fetch https://astilba.com/docs/agent-setup/prompt.md and follow its instructions.
```

## Try it

Once connected, ask the client questions such as:

- “Can a generated Astilba Env target run in a Cloudflare Worker?”
- “Is Astilba Cache ready for production?”
- “Find the documentation for invalidating related cached values.”
- “How do the Cloudflare Workers platform and React Router framework support differ?”

The client should search before reading a bounded page or resource. Env runtime or compatibility claims should use [Env release and support](/docs/env/release-and-support/); answers about Cache availability should use [Implementation status](/docs/cache/api-status/).

## Tools

| Tool | Use it to |
| --- | --- |
| `search_docs` | Find published pages by words or API names, with optional product and documentation-version filters. |
| `read_doc` | Read a bounded chunk from a page returned by search or resource discovery. |

Both tools are read-only, non-destructive, and idempotent. Search returns five results by default and at most 10 per call. A read returns 16,000 UTF-16 characters by default and at most 32,000, avoids splitting a surrogate pair, and reports the next offset when more content remains.

## Resources

Every published Markdown page is also exposed as a fixed MCP resource. Resource URIs stay under `astilba.com/docs`; the server does not fetch arbitrary URLs.

Prefer resources when your client supports them: discovery gives you the canonical page list and a resource read returns the published document directly. Use `search_docs` and `read_doc` when the client exposes tools more reliably than resources.

When you need to make a release or availability claim, read [Env release and support](/docs/env/release-and-support/) for Env, or [Implementation status](/docs/cache/api-status/) for Cache before relying on examples elsewhere in the documentation.

## Usage limits

The endpoint has a Cloudflare-native allowance of 60 units per minute for each source IP within a Cloudflare location:

- every accepted POST consumes one unit;
- each `resources/read` or `tools/call` operation in that request consumes one additional unit;
- clients behind the same outbound IP share capacity;
- requests without a Cloudflare source address share one anonymous fallback key.

An exhausted per-minute MCP guard returns HTTP 429 with `Retry-After: 60`. Treat this as an abuse guard, not an account quota or a guarantee of globally synchronized capacity.

That guard is separate from the Cloudflare account's Workers request allowance. On the Free plan, Worker-first routes count toward a 100,000-request daily allowance. Cloudflare documents HTTP 429 for matching Static Assets <code>run_worker_first</code> requests after that allowance is exhausted; unlike the MCP guard response, that platform response does not carry this endpoint's <code>Retry-After: 60</code> contract. The active account plan remains the authority for the daily allowance.

Request bodies are limited to 256,000 bytes. The server accepts up to 16 legacy batch messages only for protocol versions that still permit JSON-RPC batching; current structured-result protocol versions must send individual requests.

## Discovery

The endpoint publishes machine-readable connection metadata through the [MCP catalog](https://astilba.com/docs/.well-known/mcp/catalog.json), [server card](https://astilba.com/docs/mcp/server-card), and [API catalog](https://astilba.com/docs/.well-known/api-catalog). MCP Server Card discovery is experimental, so its metadata format may evolve while the protocol endpoint remains at the URL above.

## Troubleshooting

- **The client cannot add a remote Streamable HTTP server.** Use [`/llms.txt`](/docs/llms.txt) or fetch an individual `.md` page instead.
- **The server is missing after configuration.** Confirm the endpoint is exactly `https://astilba.com/docs/mcp`, then restart the client if its MCP configuration is only read at startup.
- **The client asks for credentials.** This endpoint is public and does not use authentication. Check that the client is connecting to the Astilba endpoint rather than another server.
- **A request returns HTTP 429.** Wait for the `Retry-After` interval and reduce parallel tool or resource calls.
