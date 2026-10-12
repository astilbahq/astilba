import { describe, expect, it, vi } from "vitest";

import worker from "../worker/index";

describe("site worker", () => {
  it("redirects the exact docs path and preserves the query", async () => {
    const assets = { fetch: vi.fn() };
    const response = await worker.fetch(
      new Request("https://astilba.com/docs?source=header"),
      { ASSETS: assets }
    );

    expect(response.status).toBe(308);
    expect(response.headers.get("location")).toBe(
      "https://astilba.com/docs/?source=header"
    );
    expect(assets.fetch).not.toHaveBeenCalled();
  });

  it("passes every other path to static assets", async () => {
    const expected = new Response("site");
    const assets = { fetch: vi.fn().mockResolvedValue(expected) };
    const request = new Request("https://astilba.com/cache/");

    const response = await worker.fetch(request, { ASSETS: assets });

    expect(response).toBe(expected);
    expect(assets.fetch).toHaveBeenCalledWith(request);
  });
});
