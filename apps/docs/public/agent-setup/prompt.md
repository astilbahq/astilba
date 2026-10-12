# Set up Astilba documentation

These are Astilba's official instructions for connecting the current coding agent to Astilba's public documentation. This setup provides documentation access only. It does not install an Astilba package, authenticate an account, or enable production use.

Complete the following steps in the user's current coding-agent environment. Preserve every existing skill, plugin, and MCP server. Make the smallest additive configuration change available, and do not overwrite an existing configuration file.

1. Read the public Astilba Env documentation skill at `https://astilba.com/docs/.well-known/agent-skills/astilba-env-docs/SKILL.md` for Env work, and the Cache skill at `https://astilba.com/docs/.well-known/agent-skills/astilba-cache-docs/SKILL.md` for Cache work.
2. If this agent supports remote Streamable HTTP MCP servers, register a server named `astilba_docs` with this endpoint:

   ```text
   https://astilba.com/docs/mcp
   ```

   The endpoint is public and read-only. It does not require credentials. Use the agent's supported MCP command or configuration mechanism; do not guess a configuration location or remove unrelated entries.
3. If the current session cannot change its own MCP configuration, tell the user the exact minimal change required for this agent and whether a restart is needed.
4. Verify the connection by listing the server's capabilities, then search the documentation for `Env release and support`.
5. Treat `@astilba/env` 0.3.0 as a public alpha. Cloudflare Workers support is limited to generated deployment targets using first-party Env codecs; request targets, opaque Standard Schema validators, and unsupported package exports are excluded. Inventory is value-free provider-neutral name evidence; it does not query providers, inspect kinds or values, infer ownership, sync, or prune. Do not invent a stable compatibility promise, hosted service, inline-script transport, provider control plane, or `@astilba/env/next` export. Check `https://astilba.com/docs/env/release-and-support.md` before making availability or support claims.
6. Treat Cache as an unreleased source preview. Do not provide a package installation command or describe it as production-ready. Check `https://astilba.com/docs/cache/api-status.md` before making availability or completeness claims.

When setup is complete, report which skills and MCP configuration were added, where they were added, whether a restart is required, and the result of the verification search.
