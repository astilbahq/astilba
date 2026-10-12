# Astilba Env public examples

These are four self-contained applications that consume exact registry `@astilba/env@0.3.0`. They deliberately do not use the source checkout, workspace links, archives, or a shared application layer.

The workspace keeps the 24-hour release-age safeguard for third-party packages. It excludes only first-party `@astilba/env`; the trusted release workflow and this adoption verifier bind the package to exact registry and GitHub Release bytes.

```sh
pnpm install --frozen-lockfile
pnpm env:check
pnpm verify:all
```

Each application is meant to be read and copied on its own. Run its `pnpm env:check` before starting it; after changing a declaration, use `pnpm env:generate` and commit the refreshed generated output.

- `node-service`: `pnpm dev` uses safe sample values. For a caller-owned production run, provide `SERVICE_API_ORIGIN` and `SERVICE_NAME` in the process environment before running `pnpm start`.
- `cloudflare-worker`: `pnpm dev` starts stock local workerd with the public sample `WORKER_ENABLED=true` in `wrangler.jsonc`.
- `next-static-shell`: `pnpm dev` uses safe local values. To exercise the production server locally, run `pnpm exec cross-env NEXT_APP_NAME=Env-static-shell pnpm build`, then `pnpm start:example`. For a caller-owned production run, provide `NEXT_APP_NAME` at build time, then canonical HTTPS `NEXT_CANONICAL_ORIGIN` and `NEXT_LABEL` at deployment time. The `/api/env` route checks a dedicated public-only target; `NEXT_SERVICE_TOKEN` remains on the separate private server target and cannot block or enter the bootstrap. Localhost and `127.0.0.1` HTTP origins are accepted only when the canonical origin is omitted during local development. This maintained independent-pnpm example uses Next's webpack builder: default Turbopack cannot resolve the exact-registry package from this layout.
- `vite`: run `pnpm exec cross-env VITE_APP_NAME=Env-Vite-shell pnpm build`, then `pnpm start:example` and open the server as exactly `localhost` or `127.0.0.1`. Those two local host forms may omit `VITE_PUBLIC_ORIGIN`; the application-owned server derives the HTTP origin from the raw `Host` header and rejects other forms. For a caller-owned production run, provide canonical HTTPS `VITE_PUBLIC_ORIGIN` and `VITE_LABEL` in the process environment before running `pnpm start`. The `/env.json` route checks a dedicated public-only target; `VITE_SERVICE_TOKEN` remains on the separate private server target and cannot block or enter the bootstrap. The server, rather than Vite dev, owns `/env.json` and never inspects forwarded host headers.

Generated `.astilba/env` output is committed on purpose. After changing an `astilba.env.ts` declaration, run `pnpm env:generate`, review the generated files, and commit them. CI runs `generate --check` before it starts an app.
