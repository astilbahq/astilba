const origin = new URL(
  process.env.ASTILBA_UI_SITE ?? "https://ui.astilba.com/"
);

const response = await fetch(origin, {
  headers: { "User-Agent": "astilba-ui-production-smoke/1.0" },
  redirect: "error",
});

if (response.status !== 200) {
  throw new Error(`Showcase returned ${response.status}, expected 200.`);
}

const html = await response.text();
for (const fragment of [
  "<title>Astilba Interface</title>",
  'content="noindex, nofollow"',
]) {
  if (!html.includes(fragment)) {
    throw new Error(`Showcase HTML is missing ${JSON.stringify(fragment)}.`);
  }
}

const expectedCsp = new Map([
  ["default-src", ["'none'"]],
  ["base-uri", ["'none'"]],
  ["connect-src", ["'self'"]],
  ["font-src", ["'self'"]],
  ["form-action", ["'self'"]],
  ["frame-ancestors", ["'none'"]],
  ["img-src", ["'self'", "data:"]],
  ["script-src", ["'self'"]],
  ["style-src", ["'self'"]],
  ["object-src", ["'none'"]],
  ["upgrade-insecure-requests", []],
]);

const parseCsp = (value) => {
  const directives = new Map();
  for (const source of value.split(";")) {
    const directive = source.trim();
    if (!directive) {
      continue;
    }

    const [name, ...values] = directive.split(/\s+/u);
    if (directives.has(name)) {
      throw new Error(`Showcase content-security-policy repeats ${name}.`);
    }
    directives.set(name, values);
  }
  return directives;
};

const csp = response.headers.get("content-security-policy");
if (!csp) {
  throw new Error("Showcase content-security-policy is missing.");
}
const actualCsp = parseCsp(csp);
if (actualCsp.size !== expectedCsp.size) {
  throw new Error(
    "Showcase content-security-policy has unexpected directives."
  );
}
for (const [name, expectedValues] of expectedCsp) {
  const actualValues = actualCsp.get(name);
  if (
    !actualValues ||
    JSON.stringify(actualValues) !== JSON.stringify(expectedValues)
  ) {
    throw new Error(`Showcase content-security-policy has an unsafe ${name}.`);
  }
}

const requiredHeaders = new Map([
  [
    "permissions-policy",
    "accelerometer=(), ambient-light-sensor=(), autoplay=(), bluetooth=(), camera=(), clipboard-read=(), clipboard-write=(), display-capture=(), encrypted-media=(), fullscreen=(), gamepad=(), geolocation=(), gyroscope=(), hid=(), magnetometer=(), microphone=(), midi=(), payment=(), picture-in-picture=(), publickey-credentials-create=(), publickey-credentials-get=(), screen-wake-lock=(), serial=(), usb=(), web-share=(), xr-spatial-tracking=()",
  ],
  ["referrer-policy", "strict-origin-when-cross-origin"],
  ["strict-transport-security", "max-age=31536000"],
  ["x-content-type-options", "nosniff"],
  ["x-frame-options", "DENY"],
  ["x-robots-tag", "noindex, nofollow"],
]);

for (const [name, expectedValue] of requiredHeaders) {
  const value = response.headers.get(name);
  if (value !== expectedValue) {
    throw new Error(
      `Showcase ${name} must be ${JSON.stringify(expectedValue)}.`
    );
  }
}

const assetPath =
  /<script\b[^>]*\bsrc="(?<assetPath>\/assets\/[^"]+\.js)"/u.exec(html)?.groups
    ?.assetPath;
if (!assetPath) {
  throw new Error("Showcase HTML does not reference a fingerprinted asset.");
}

const assetResponse = await fetch(new URL(assetPath, origin), {
  headers: { "User-Agent": "astilba-ui-production-smoke/1.0" },
  redirect: "error",
});
if (assetResponse.status !== 200) {
  throw new Error(
    `Showcase asset returned ${assetResponse.status}, expected 200.`
  );
}
if (
  assetResponse.headers.get("cache-control") !==
  "public, max-age=31536000, immutable"
) {
  throw new Error("Showcase asset is missing immutable caching.");
}
const script = await assetResponse.text();
for (const fragment of ["https://astilba.com/", "Astilba home"]) {
  if (!script.includes(fragment)) {
    throw new Error(`Showcase script is missing ${JSON.stringify(fragment)}.`);
  }
}

const robotsResponse = await fetch(new URL("/robots.txt", origin), {
  headers: { "User-Agent": "astilba-ui-production-smoke/1.0" },
  redirect: "error",
});
const robots = await robotsResponse.text();
if (
  robotsResponse.status !== 200 ||
  robots.trim() !== "User-agent: *\nDisallow: /"
) {
  throw new Error("Showcase robots.txt does not prevent indexing.");
}

console.log(`Astilba UI production smoke passed for ${origin.href}.`);
