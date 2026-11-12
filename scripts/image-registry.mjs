import { createHash } from "node:crypto";
import {
  FetchFailure,
  outlastOutage,
  withFetchAttempts,
} from "./fetch-attempts.mjs";

// Reads image manifests, tags and configs through a registry's distribution API. See
// scripts/pin-report.mjs.

// Every host and CI runner is one of these.
export const platforms = ["linux/amd64", "linux/arm64"];
const manifestTypes = [
  "application/vnd.oci.image.index.v1+json",
  "application/vnd.docker.distribution.manifest.list.v2+json",
  "application/vnd.oci.image.manifest.v1+json",
  "application/vnd.docker.distribution.manifest.v2+json",
].join(", ");

// GitLab's repository, the largest pinned, has fewer than 20 pages of 1000 tags.
const maxTagPages = 100;

const sha256 = (bytes) =>
  `sha256:${createHash("sha256").update(bytes).digest("hex")}`;

// Asks a registry's distribution API as an anonymous client, taking the bearer token
// its challenge offers. A dropped connection, a rate limit or a server error repeats
// as other downloads do.
export function registry(fetchImpl = fetch, delay = outlastOutage) {
  const tokens = new Map();
  const host = (name) => (name === "docker.io" ? "registry-1.docker.io" : name);
  const attempt = async (url, init) => {
    let response;
    try {
      response = await fetchImpl(url, init);
    } catch (error) {
      throw new FetchFailure(error.cause?.message || error.message);
    }
    if (response.status === 429 || response.status >= 500) {
      await response.body?.cancel();
      throw new FetchFailure(`${url} returned ${response.status}`);
    }
    return response;
  };
  async function token(challenge, repository) {
    const fields = Object.fromEntries(
      [...challenge.matchAll(/(\w+)="([^"]*)"/g)].map(([, key, value]) => [
        key,
        value,
      ]),
    );
    if (!/^bearer /i.test(challenge) || !fields.realm?.startsWith("https://"))
      throw new Error(`unexpected registry challenge: ${challenge}`);
    const url = new URL(fields.realm);
    if (fields.service) url.searchParams.set("service", fields.service);
    url.searchParams.set(
      "scope",
      fields.scope ?? `repository:${repository}:pull`,
    );
    const response = await attempt(url, {});
    if (!response.ok)
      throw new Error(`${url.origin} refused a token: ${response.status}`);
    const body = await response.json();
    return body.token ?? body.access_token;
  }
  async function request(pin, path, { method = "GET", accept } = {}) {
    const url = `https://${host(pin.registry)}/v2/${pin.repository}/${path}`;
    const key = `${pin.registry}/${pin.repository}`;
    return withFetchAttempts(
      `${method} ${url}`,
      async () => {
        for (let tries = 0; ; tries++) {
          const headers = { ...(accept && { accept }) };
          if (tokens.has(key))
            headers.authorization = `Bearer ${tokens.get(key)}`;
          const response = await attempt(url, { method, headers });
          if (response.status === 401 && tries === 0) {
            await response.body?.cancel();
            tokens.set(
              key,
              await token(
                response.headers.get("www-authenticate") ?? "",
                pin.repository,
              ),
            );
            continue;
          }
          if (!response.ok) {
            await response.body?.cancel();
            throw new Error(`${url} returned ${response.status}`);
          }
          return response;
        }
      },
      { delay },
    );
  }
  return {
    // A manifest by digest is checked against that digest, so the registry cannot
    // describe different contents.
    async manifest(pin, digest) {
      const bytes = Buffer.from(
        await (
          await request(pin, `manifests/${digest}`, { accept: manifestTypes })
        ).arrayBuffer(),
      );
      if (sha256(bytes) !== digest)
        throw new Error(`${pin.image}@${digest} returned other bytes`);
      return JSON.parse(bytes);
    },
    async tagDigest(pin) {
      const response = await request(pin, `manifests/${pin.tag}`, {
        method: "HEAD",
        accept: manifestTypes,
      });
      const digest = response.headers.get("docker-content-digest");
      if (/^sha256:[0-9a-f]{64}$/.test(digest ?? "")) return digest;
      const bytes = Buffer.from(
        await (
          await request(pin, `manifests/${pin.tag}`, { accept: manifestTypes })
        ).arrayBuffer(),
      );
      return sha256(bytes);
    },
    async blob(pin, digest) {
      const bytes = Buffer.from(
        await (await request(pin, `blobs/${digest}`)).arrayBuffer(),
      );
      if (sha256(bytes) !== digest)
        throw new Error(`${pin.image} blob ${digest} returned other bytes`);
      return JSON.parse(bytes);
    },
    // A registry that keeps naming a next page, or the same one, stops here rather
    // than holding up every other image.
    async tags(pin) {
      const tags = [];
      const seen = new Set();
      for (let path = "tags/list?n=1000"; path;) {
        if (seen.has(path) || seen.size === maxTagPages)
          throw new Error(`${pin.image} lists tags past ${seen.size} pages`);
        seen.add(path);
        const response = await request(pin, path);
        tags.push(...((await response.json()).tags ?? []));
        const next = /<[^>]*\/tags\/list(\?[^>]*)>;\s*rel="?next"?/.exec(
          response.headers.get("link") ?? "",
        )?.[1];
        path = next && `tags/list${next}`;
      }
      return tags;
    },
  };
}

// The single-platform manifests a pin stands for on the platforms Astilba runs.
export async function platformManifests(client, pin, manifest) {
  if (!manifest.manifests) {
    const config = await client.blob(pin, manifest.config.digest);
    const platform = `${config.os}/${config.architecture}`;
    return platforms.includes(platform)
      ? [{ platform, digest: pin.digest }]
      : [];
  }
  return platforms.flatMap((platform) => {
    const [os, architecture] = platform.split("/");
    const entry = manifest.manifests.find(
      ({ platform: p }) =>
        p?.os === os &&
        p?.architecture === architecture &&
        (architecture !== "arm64" || [undefined, "v8"].includes(p.variant)),
    );
    return entry ? [{ platform, digest: entry.digest }] : [];
  });
}
