// SPDX-License-Identifier: AGPL-3.0-only
import { DurableObject } from 'cloudflare:workers';

interface Env {
  MANAGEMENT: DurableObjectNamespace<Management>;
  ASSETS: Fetcher;
  PLAT6_PUBLIC_ORIGIN: string;
  PLAT6_DATABASE_URL: string;
  PLAT6_LOGIN_TOKEN: string;
  PLAT6_INTERNAL_TOKEN: string;
  PLAT6_AGENT_SEED: string;
}

const idleMilliseconds = 30_000;

export class Management extends DurableObject<Env> {
  private starting: Promise<void> | undefined;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    if (ctx.container?.running) {
      ctx.blockConcurrencyWhile(() => ctx.container!.setInactivityTimeout(idleMilliseconds));
    }
  }

  private async ready(): Promise<void> {
    const container = this.ctx.container!;
    if (!container.running) {
      container.start({
        image: container.images.management,
        instance: 'lite',
        enableInternet: true,
        env: {
          PLAT6_PUBLIC_ORIGIN: this.env.PLAT6_PUBLIC_ORIGIN,
          PLAT6_DATABASE_URL: this.env.PLAT6_DATABASE_URL,
          PLAT6_LOGIN_TOKEN: this.env.PLAT6_LOGIN_TOKEN,
          PLAT6_INTERNAL_TOKEN: this.env.PLAT6_INTERNAL_TOKEN,
          PLAT6_AGENT_SEED: this.env.PLAT6_AGENT_SEED,
        },
      });
      await container.setInactivityTimeout(idleMilliseconds);
    }
    const port = container.getTcpPort(8080);
    for (let attempt = 0; attempt < 100; attempt++) {
      try {
        const response = await port.fetch('http://container/health', {
          signal: AbortSignal.timeout(1000),
        });
        if (response.ok) return;
        await response.body?.cancel();
      } catch { /* The process may not yet have bound its port. */ }
      await scheduler.wait(200);
    }
    throw new Error('Management readiness timed out');
  }

  async stop(): Promise<void> {
    await this.ctx.container!.destroy();
  }

  async running(): Promise<boolean> {
    return this.ctx.container!.running;
  }

  async fetch(request: Request): Promise<Response> {
    this.starting ??= this.ready().finally(() => { this.starting = undefined; });
    await this.starting;
    const url = new URL(request.url);
    url.protocol = 'http:';
    url.host = 'container';
    const headers = new Headers(request.headers);
    for (const name of ['host', 'forwarded', 'x-forwarded-host', 'x-forwarded-proto', 'x-forwarded-for']) {
      headers.delete(name);
    }
    return this.ctx.container!.getTcpPort(8080).fetch(new Request(url, {
      method: request.method,
      headers,
      body: request.method === 'GET' || request.method === 'HEAD' ? undefined : request.body,
      redirect: 'manual',
      signal: request.signal,
    }));
  }
}

async function hasControlToken(request: Request, env: Env): Promise<boolean> {
  if (!env.PLAT6_INTERNAL_TOKEN || env.PLAT6_INTERNAL_TOKEN.length < 32) return false;
  const encoder = new TextEncoder();
  const [actual, expected] = await Promise.all([
    crypto.subtle.digest('SHA-256', encoder.encode(request.headers.get('authorization') ?? '')),
    crypto.subtle.digest('SHA-256', encoder.encode(`Bearer ${env.PLAT6_INTERNAL_TOKEN}`)),
  ]);
  return crypto.subtle.timingSafeEqual(actual, expected);
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    const management = env.MANAGEMENT.getByName('plat6-fixture');
    if (url.pathname.startsWith('/_fixture/')) {
      if (!await hasControlToken(request, env)) return new Response('Unauthorized', {status: 401});
      if (url.pathname === '/_fixture/stop' && request.method === 'POST') {
        await management.stop();
        return new Response(null, {status: 204});
      }
      if (url.pathname === '/_fixture/status' && request.method === 'GET') {
        return Response.json({running: await management.running()}, {headers: {'Cache-Control': 'no-store'}});
      }
      if (url.pathname === '/_fixture/metrics' && request.method === 'GET') {
        return management.fetch(request);
      }
      return new Response('Not found', {status: 404});
    }
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/v1/') || url.pathname === '/health') {
      return management.fetch(request);
    }
    return env.ASSETS.fetch(request);
  },
  async scheduled(_event: ScheduledController, env: Env): Promise<void> {
    const response = await env.MANAGEMENT.getByName('plat6-fixture').fetch(new Request('https://fixture.invalid/_fixture/wake', {
      method: 'POST',
      headers: {'Authorization': `Bearer ${env.PLAT6_INTERNAL_TOKEN}`},
    }));
    if (!response.ok) throw new Error('Management wake failed');
  },
} satisfies ExportedHandler<Env>;
