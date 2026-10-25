import { DurableObject } from "cloudflare:workers";

interface Env {
  MANAGEMENT: DurableObjectNamespace<Management>;
  ASTILBA_ORIGIN: string;
  ASTILBA_DATABASE_URL: string;
  ASTILBA_KEYRING_JSON: string;
  ASTILBA_TRUSTED_PROXIES?: string;
  ASTILBA_APPLICATION_DOMAIN?: string;
  ASTILBA_DNS_CLOUDFLARE_ZONE?: string;
  ASTILBA_DNS_CLOUDFLARE_TOKEN?: string;
  ASTILBA_HOSTED?: string;
  ASTILBA_HOSTED_COHORT_LIMIT?: string;
  ASTILBA_HOSTED_SERVER_LIMIT?: string;
  ASTILBA_HOSTED_SUPPORT?: string;
  ASTILBA_STRIPE_PRICE?: string;
  ASTILBA_STRIPE_SECRET?: string;
  ASTILBA_STRIPE_WEBHOOK_SECRET?: string;
  ASTILBA_GITHUB_APP_ID?: string;
  ASTILBA_GITHUB_APP_CLIENT_ID?: string;
  ASTILBA_GITHUB_APP_SLUG?: string;
  ASTILBA_GITHUB_APP_CLIENT_SECRET?: string;
  ASTILBA_GITHUB_APP_WEBHOOK_SECRET?: string;
  ASTILBA_GITHUB_APP_PRIVATE_KEY?: string;
  ASTILBA_SMTP_ADDRESS?: string;
  ASTILBA_SMTP_MODE?: string;
  ASTILBA_SMTP_FROM?: string;
  ASTILBA_SMTP_USERNAME?: string;
  ASTILBA_SMTP_PASSWORD?: string;
}

export class Management extends DurableObject<Env> {
  private starting: Promise<void> | undefined;

  private async ready(): Promise<void> {
    const container = this.ctx.container!;
    if (!container.running) {
      const env: Record<string, string> = { ASTILBA_LISTEN: "0.0.0.0:8080" };
      for (const [key, value] of Object.entries(this.env)) {
        if (key.startsWith("ASTILBA_") && typeof value === "string") env[key] = value;
      }
      container.start({ image: container.images.management, instance: "lite", enableInternet: true, env });
    }
    await container.setInactivityTimeout(5 * 60_000);
    const port = container.getTcpPort(8080);
    for (let attempt = 0; attempt < 100; attempt++) {
      try {
        const response = await port.fetch("http://container/healthz", { signal: AbortSignal.timeout(1000) });
        if (response.ok) return;
        await response.body?.cancel();
      } catch { /* Wait for the process to bind its port. */ }
      await scheduler.wait(200);
    }
    throw new Error("Management readiness timed out");
  }

  async fetch(request: Request): Promise<Response> {
    this.starting ??= this.ready().finally(() => { this.starting = undefined; });
    await this.starting;
    const url = new URL(request.url); url.protocol = "http:"; url.host = "container";
    const headers = new Headers(request.headers);
    for (const name of ["host", "forwarded", "x-forwarded-host", "x-forwarded-proto", "x-forwarded-for"]) headers.delete(name);
    const clientIP = request.headers.get("cf-connecting-ip");
    if (clientIP) headers.set("x-forwarded-for", clientIP);
    return this.ctx.container!.getTcpPort(8080).fetch(new Request(url, {
      method: request.method, headers,
      body: ["GET", "HEAD"].includes(request.method) ? undefined : request.body,
      redirect: "manual", signal: request.signal,
    }));
  }
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    try { return await env.MANAGEMENT.getByName("installation").fetch(request); }
    catch { return new Response("Management unavailable", { status: 503, headers: { "Cache-Control": "no-store" } }); }
  },
  async scheduled(_event: ScheduledController, env: Env): Promise<void> {
    const response = await env.MANAGEMENT.getByName("installation").fetch(new Request("https://keepalive.invalid/healthz"));
    await response.body?.cancel();
    if (!response.ok) throw new Error("Management keepalive failed");
  },
} satisfies ExportedHandler<Env>;
