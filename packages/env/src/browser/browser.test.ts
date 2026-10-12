/* oxlint-disable typescript/no-unsafe-type-assertion, typescript/promise-function-async -- Exact protocol tests intentionally assert complete descriptor and observation matrices, construct hostile erased inputs, and use deterministic promise-returning mocks without artificial async scheduling. */

import { describe, expect, it, vi } from "vitest";

import {
  BOOTSTRAP_PROTOCOL,
  BootstrapFailure,
  loadBrowserBootstrap,
  parseBrowserBootstrap,
  startBrowserApplication,
} from "./index.ts";
import type { BrowserProjection } from "./index.ts";

const ORIGIN = "https://tenant.example";
const DIGEST = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";

type Configuration = Readonly<{
  apiOrigin: string;
}>;

type Decoder = (
  input: Readonly<Record<string, unknown>>,
  failure: (code: unknown) => never
) => unknown;

const configuration = (
  values: Readonly<Record<string, unknown>>
): Readonly<Record<string, unknown>> => {
  const output = Object.create(null) as Record<string, unknown>;
  for (const name of Object.keys(values).toSorted()) {
    output[name] = values[name];
  }
  return Object.freeze(output);
};

const generatedProjectionRecord = (
  decode: Decoder = (input, failure) => {
    if (Object.keys(input).some((name) => name !== "apiOrigin")) {
      return failure("BOOTSTRAP_UNKNOWN_FIELD");
    }
    if (!Object.hasOwn(input, "apiOrigin")) {
      return failure("BOOTSTRAP_VALUE_MISSING");
    }
    if (typeof input.apiOrigin !== "string") {
      return failure("BOOTSTRAP_VALUE_INVALID");
    }
    return configuration({ apiOrigin: input.apiOrigin });
  }
): Readonly<Record<string, unknown>> => ({
  codecAbi: "astilba.env.codec/v1",
  consumer: "web",
  contract: "example.platform",
  decode,
  digest: DIGEST,
  format: "astilba.env.projection",
  formatVersion: 1,
  generated: "astilba.env.generated-module/v1",
  kind: "public",
  lifecycle: "deployment",
  projectionAbi: "astilba.env.projection/v1",
});

const projection = (decode?: Decoder): BrowserProjection<Configuration> =>
  Object.freeze(
    generatedProjectionRecord(decode)
  ) as unknown as BrowserProjection<Configuration>;

const envelope = (overrides: Readonly<Record<string, unknown>> = {}): string =>
  JSON.stringify({
    audience: { origin: ORIGIN },
    consumer: "web",
    contract: "example.platform",
    lifecycle: "deployment",
    projection: DIGEST,
    protocol: BOOTSTRAP_PROTOCOL,
    values: { apiOrigin: "https://api.example" },
    ...overrides,
  });

const parse = (
  source: string,
  selectedProjection = projection()
): ReturnType<typeof parseBrowserBootstrap<Configuration>> =>
  parseBrowserBootstrap({
    expectedAudience: { origin: ORIGIN },
    projection: selectedProjection,
    source,
  });

const failureCode = (operation: () => unknown): string => {
  try {
    operation();
  } catch (error) {
    expect(error).toBeInstanceOf(BootstrapFailure);
    return (error as BootstrapFailure).code;
  }
  throw new Error("Expected a bootstrap failure.");
};

const asyncFailureCode = async (
  operation: () => Promise<unknown>
): Promise<string> => {
  try {
    await operation();
  } catch (error) {
    expect(error).toBeInstanceOf(BootstrapFailure);
    return (error as BootstrapFailure).code;
  }
  throw new Error("Expected a bootstrap failure.");
};

const responseFor = (source: string): Readonly<Record<string, unknown>> => {
  const bytes = new TextEncoder().encode(source);
  let readCount = 0;
  return {
    body: {
      getReader() {
        return {
          cancel: vi.fn<() => Promise<void>>(() => Promise.resolve()),
          read: vi.fn<
            () => Promise<{ done: false; value: Uint8Array } | { done: true }>
          >(() => {
            readCount += 1;
            return Promise.resolve(
              readCount === 1 ? { done: false, value: bytes } : { done: true }
            );
          }),
        };
      },
    },
    headers: {
      get(name: string) {
        return name === "content-type"
          ? "application/json; charset=utf-8"
          : null;
      },
    },
    redirected: false,
    status: 200,
    url: `${ORIGIN}/bootstrap`,
  };
};

const responseForChunks = (chunks: readonly Uint8Array[]) => {
  let index = 0;
  const cancel = vi.fn<() => Promise<void>>(() => Promise.resolve());
  const read = vi.fn<
    () => Promise<{ done: false; value: Uint8Array } | { done: true }>
  >(() => {
    const value = chunks[index];
    index += 1;
    return Promise.resolve(
      value === undefined ? { done: true } : { done: false, value }
    );
  });
  return {
    cancel,
    read,
    response: {
      body: {
        getReader: () => ({ cancel, read }),
      },
      headers: {
        get: () => "application/json",
      },
      redirected: false,
      status: 200,
      url: `${ORIGIN}/bootstrap`,
    },
  };
};

const loadFromResponse = (
  response: Readonly<Record<string, unknown>>
): ReturnType<typeof loadBrowserBootstrap<Configuration>> =>
  loadBrowserBootstrap({
    endpoint: "/bootstrap",
    expectedAudience: { origin: ORIGIN },
    fetch: vi.fn(() =>
      Promise.resolve(response)
    ) as unknown as typeof globalThis.fetch,
    projection: projection(),
    requestBaseUrl: ORIGIN,
  });

describe("browser bootstrap", () => {
  it("returns exact fresh owned success records", () => {
    const expectedAudience = { origin: ORIGIN };
    const result = parseBrowserBootstrap({
      expectedAudience,
      projection: projection(),
      source: envelope(),
    });

    expect(Reflect.ownKeys(result)).toStrictEqual(["audience", "values"]);
    expect(Object.getPrototypeOf(result)).toBe(Object.prototype);
    expect(Object.isFrozen(result)).toBe(true);
    expect(result.audience).not.toBe(expectedAudience);
    expect(Reflect.ownKeys(result.audience)).toStrictEqual(["origin"]);
    expect(Object.getPrototypeOf(result.audience)).toBe(Object.prototype);
    expect(Object.isFrozen(result.audience)).toBe(true);
    expect(Object.getPrototypeOf(result.values)).toBeNull();
    expect(Object.isFrozen(result.values)).toBe(true);
    for (const name of ["audience", "values"] as const) {
      expect(Object.getOwnPropertyDescriptor(result, name)).toStrictEqual({
        configurable: false,
        enumerable: true,
        value: result[name],
        writable: false,
      });
    }
  });

  it("refuses a newer generated format before later observation", () => {
    let laterObservations = 0;
    const hostile = new Proxy(
      {},
      {
        getOwnPropertyDescriptor(_target, key) {
          if (key === "generated") {
            return {
              configurable: true,
              enumerable: true,
              value: "astilba.env.generated-module/v2",
              writable: true,
            };
          }
          laterObservations += 1;
          throw new Error("unexpected descriptor observation");
        },
        getPrototypeOf() {
          laterObservations += 1;
          throw new Error("unexpected prototype observation");
        },
        ownKeys() {
          return ["generated", "decode", "future"];
        },
      }
    ) as unknown as BrowserProjection<Configuration>;
    const options = {
      get expectedAudience(): never {
        laterObservations += 1;
        throw new Error("unexpected audience observation");
      },
      projection: hostile,
      get source(): never {
        laterObservations += 1;
        throw new Error("unexpected source observation");
      },
    };

    expect(
      failureCode(() =>
        parseBrowserBootstrap(
          options as unknown as Parameters<
            typeof parseBrowserBootstrap<Configuration>
          >[0]
        )
      )
    ).toBe("BOOTSTRAP_GENERATED_FORMAT_UNSUPPORTED");
    expect(laterObservations).toBe(0);
  });

  it("captures every staged parser input exactly once", () => {
    const descriptorReads = new Map<PropertyKey, number>();
    let ownKeyReads = 0;
    let prototypeReads = 0;
    const selectedProjection = new Proxy(generatedProjectionRecord(), {
      getOwnPropertyDescriptor(target, key) {
        descriptorReads.set(key, (descriptorReads.get(key) ?? 0) + 1);
        return Reflect.getOwnPropertyDescriptor(target, key);
      },
      getPrototypeOf(target) {
        prototypeReads += 1;
        return Reflect.getPrototypeOf(target);
      },
      ownKeys(target) {
        ownKeyReads += 1;
        return Reflect.ownKeys(target);
      },
    }) as unknown as BrowserProjection<Configuration>;
    const reads = {
      audience: 0,
      origin: 0,
      projection: 0,
      source: 0,
    };
    const expectedAudience = {
      get origin(): string {
        reads.origin += 1;
        return ORIGIN;
      },
    };
    const options = {
      get expectedAudience() {
        reads.audience += 1;
        return expectedAudience;
      },
      get projection() {
        reads.projection += 1;
        return selectedProjection;
      },
      get source() {
        reads.source += 1;
        return envelope();
      },
    };

    parseBrowserBootstrap(options);
    expect(reads).toStrictEqual({
      audience: 1,
      origin: 1,
      projection: 1,
      source: 1,
    });
    expect(ownKeyReads).toBe(1);
    expect(prototypeReads).toBe(1);
    expect([...descriptorReads.values()]).toStrictEqual(
      Array.from({ length: 11 }, () => 1)
    );
  });

  it("completes agreement before decoding value names", () => {
    expect(
      failureCode(() =>
        parse(
          envelope({
            contract: "other.platform",
            values: { unexpected: true },
          })
        )
      )
    ).toBe("BOOTSTRAP_CONTRACT_MISMATCH");
    expect(
      failureCode(() => parse(envelope({ values: { unexpected: true } })))
    ).toBe("BOOTSTRAP_UNKNOWN_FIELD");
  });

  it("makes generated callback failure monotonic per invocation", () => {
    const swallowingProjection = projection((_input, fail) => {
      try {
        fail("BOOTSTRAP_VALUE_MISSING");
      } catch {
        // Generated code may attempt to swallow its issued failure.
      }
      return configuration({});
    });
    expect(failureCode(() => parse(envelope(), swallowingProjection))).toBe(
      "BOOTSTRAP_VALUE_MISSING"
    );

    let invocation = 0;
    let staleFailure: unknown;
    const staleProjection = projection((_input, fail) => {
      invocation += 1;
      if (invocation === 1) {
        try {
          fail("BOOTSTRAP_UNKNOWN_FIELD");
        } catch (error) {
          staleFailure = error;
          throw error;
        }
      }
      // oxlint-disable-next-line eslint/no-throw-literal -- The test rethrows an exact prior generated failure identity.
      throw staleFailure;
    });
    expect(failureCode(() => parse(envelope(), staleProjection))).toBe(
      "BOOTSTRAP_UNKNOWN_FIELD"
    );
    expect(failureCode(() => parse(envelope(), staleProjection))).toBe(
      "BOOTSTRAP_VALUE_INVALID"
    );
  });

  it("applies parser crossing-condition precedence", () => {
    const names = [
      '"a":0',
      ...Array.from({ length: 255 }, (_, index) => `"k${index}":0`),
    ];
    expect(failureCode(() => parse(`{${names.join(",")},"a":0}`))).toBe(
      "BOOTSTRAP_DUPLICATE_KEY"
    );
    expect(failureCode(() => parse(`{${names.join(",")},"\\u0061":0}`))).toBe(
      "BOOTSTRAP_DUPLICATE_KEY"
    );
    expect(failureCode(() => parse(`{${names.join(",")},"__proto__":0}`))).toBe(
      "BOOTSTRAP_NON_PORTABLE_JSON"
    );
    expect(
      failureCode(() => parse(`{${names.join(",")},"\\u005f_proto__":0}`))
    ).toBe("BOOTSTRAP_NON_PORTABLE_JSON");

    const nested = `${"[".repeat(8)}${Array.from(
      { length: 1017 },
      () => "0"
    ).join(",")},[]${"]".repeat(8)}`;
    expect(failureCode(() => parse(nested))).toBe(
      "BOOTSTRAP_NON_PORTABLE_JSON"
    );
  });

  it("refuses non-portable primitives before envelope agreement", () => {
    const source = envelope({ contract: "other.platform" });
    const withValue = (raw: string): string =>
      source.replace('"https://api.example"', raw);

    for (const raw of ["-0", "1.5", "1e0", "9007199254740992"]) {
      expect(failureCode(() => parse(withValue(raw)))).toBe(
        "BOOTSTRAP_INVALID_JSON"
      );
    }
    for (const code of [0xd8_00, 0xdc_00]) {
      expect(
        failureCode(() =>
          parse(
            envelope({
              contract: "other.platform",
              values: { apiOrigin: String.fromCharCode(code) },
            })
          )
        )
      ).toBe("BOOTSTRAP_NON_PORTABLE_JSON");
    }

    const astral = String.fromCodePoint(0x1_f6_00);
    expect(
      parse(
        envelope({
          values: { apiOrigin: astral },
        })
      ).values.apiOrigin
    ).toBe(astral);
  });

  it("guards construction and fixes failure descriptors", () => {
    let caught: unknown;
    try {
      Reflect.construct(BootstrapFailure as unknown as new () => unknown, []);
    } catch (error) {
      caught = error;
    }
    expect(caught).toBeInstanceOf(TypeError);
    expect((caught as Error).message).toBe(
      "BootstrapFailure cannot be constructed directly."
    );

    let bootstrap: BootstrapFailure | undefined;
    try {
      parse("{");
    } catch (error) {
      bootstrap = error as BootstrapFailure;
    }
    expect(bootstrap).toBeInstanceOf(BootstrapFailure);
    expect(Object.getOwnPropertyDescriptor(bootstrap, "name")).toStrictEqual({
      configurable: false,
      enumerable: false,
      value: "BootstrapFailure",
      writable: false,
    });
    expect(Object.getOwnPropertyDescriptor(bootstrap, "message")).toStrictEqual(
      {
        configurable: false,
        enumerable: false,
        value: "BOOTSTRAP_INVALID_JSON",
        writable: false,
      }
    );
    expect(Object.getOwnPropertyDescriptor(bootstrap, "code")).toStrictEqual({
      configurable: false,
      enumerable: true,
      value: "BOOTSTRAP_INVALID_JSON",
      writable: false,
    });
  });

  it("remaps forged decoder failures to authentic cause-free failures", () => {
    const forged = Object.create(
      BootstrapFailure.prototype
    ) as BootstrapFailure;
    Object.defineProperty(forged, "code", {
      enumerable: true,
      value: "BOOTSTRAP_VALUE_MISSING",
    });
    let observed: unknown;
    try {
      parse(
        envelope(),
        projection(() => {
          throw forged;
        })
      );
    } catch (error) {
      observed = error;
    }

    expect(observed).toBeInstanceOf(BootstrapFailure);
    expect(observed).not.toBe(forged);
    expect(Object.getPrototypeOf(observed)).toBe(BootstrapFailure.prototype);
    expect((observed as BootstrapFailure).code).toBe("BOOTSTRAP_VALUE_INVALID");
    expect(Object.hasOwn(observed as object, "cause")).toBe(false);
  });

  it("uses the explicit one-shot no-store transport", async () => {
    let receiver: unknown;
    let receivedInit: RequestInit | undefined;
    let receivedRequest: URL | undefined;
    const captureReceiver = (value: unknown): void => {
      receiver = value;
    };
    const fetch = vi.fn<
      (
        this: unknown,
        request: URL,
        init: RequestInit
      ) => Promise<Readonly<Record<string, unknown>>>
    >(function fetch(this: unknown, request: URL, init: RequestInit) {
      captureReceiver(this);
      receivedRequest = request;
      receivedInit = init;
      return Promise.resolve(responseFor(envelope()));
    });
    const result = await loadBrowserBootstrap({
      endpoint: "/bootstrap",
      expectedAudience: { origin: ORIGIN },
      fetch: fetch as unknown as typeof globalThis.fetch,
      projection: projection(),
      requestBaseUrl: `${ORIGIN}/application`,
    });

    expect(fetch).toHaveBeenCalledOnce();
    expect(receiver).toBeUndefined();
    expect(receivedRequest).toBeInstanceOf(URL);
    expect(receivedRequest?.href).toBe(`${ORIGIN}/bootstrap`);
    expect(receivedInit).toStrictEqual({
      cache: "no-store",
      redirect: "error",
    });
    expect(result.values.apiOrigin).toBe("https://api.example");
  });

  it("reads response fields once in order and stops after early failure", async () => {
    const reads: string[] = [];
    let headersReceiver: unknown;
    const body = responseFor(envelope()).body;
    const headers = {
      get get() {
        reads.push("headers.get");
        return function get(this: unknown, name: string) {
          reads.push(`headers.get:${name}`);
          headersReceiver = this;
          return "application/json";
        };
      },
    };
    const response = {
      get body() {
        reads.push("response.body");
        return body;
      },
      get headers() {
        reads.push("response.headers");
        return headers;
      },
      get redirected() {
        reads.push("response.redirected");
        return false;
      },
      get status() {
        reads.push("response.status");
        return 200;
      },
      get url() {
        reads.push("response.url");
        return `${ORIGIN}/bootstrap`;
      },
    };

    await expect(loadFromResponse(response)).resolves.toMatchObject({
      values: { apiOrigin: "https://api.example" },
    });
    expect(reads).toStrictEqual([
      "response.redirected",
      "response.status",
      "response.url",
      "response.headers",
      "headers.get",
      "headers.get:content-type",
      "response.body",
    ]);
    expect(headersReceiver).toBe(headers);

    reads.length = 0;
    const earlyFailure = {
      get redirected() {
        reads.push("response.redirected");
        return true;
      },
      get status(): never {
        reads.push("response.status");
        throw new Error("must not be read");
      },
      get url(): never {
        reads.push("response.url");
        throw new Error("must not be read");
      },
    };
    await expect(
      asyncFailureCode(() => loadFromResponse(earlyFailure))
    ).resolves.toBe("BOOTSTRAP_REDIRECTED");
    expect(reads).toStrictEqual(["response.redirected"]);
  });

  it("captures reader methods once and skips value after done", async () => {
    const reads = {
      body: 0,
      cancel: 0,
      done: 0,
      getReader: 0,
      read: 0,
      value: 0,
    };
    let getReaderReceiver: unknown;
    let readReceiver: unknown;
    const result = {
      get done() {
        reads.done += 1;
        return true;
      },
      get value(): never {
        reads.value += 1;
        throw new Error("must not be read after done");
      },
    };
    const reader = {
      get cancel() {
        reads.cancel += 1;
        return () => Promise.resolve();
      },
      get read() {
        reads.read += 1;
        return function read(this: unknown) {
          readReceiver = this;
          return Promise.resolve(result);
        };
      },
    };
    const body = {
      get getReader() {
        reads.getReader += 1;
        return function getReader(this: unknown) {
          getReaderReceiver = this;
          return reader;
        };
      },
    };
    const response = {
      ...responseFor(envelope()),
      get body() {
        reads.body += 1;
        return body;
      },
    };

    await expect(
      asyncFailureCode(() => loadFromResponse(response))
    ).resolves.toBe("BOOTSTRAP_INVALID_JSON");
    expect(reads).toStrictEqual({
      body: 1,
      cancel: 1,
      done: 1,
      getReader: 1,
      read: 1,
      value: 0,
    });
    expect(getReaderReceiver).toBe(body);
    expect(readReceiver).toBe(reader);
  });

  it("cancels on an initial BOM before same-chunk overflow", async () => {
    const cancel = vi.fn<() => Promise<never>>(() =>
      Promise.reject(new Error("ignored"))
    );
    const read = vi.fn<() => Promise<{ done: false; value: Uint8Array }>>(() =>
      Promise.resolve({
        done: false,
        value: Uint8Array.from([0xef, 0xbb, 0xbf, ...new Uint8Array(65_534)]),
      })
    );
    const fetch = vi.fn<() => Promise<Readonly<Record<string, unknown>>>>(() =>
      Promise.resolve({
        body: {
          getReader: () => ({ cancel, read }),
        },
        headers: {
          get: () => "application/json",
        },
        redirected: false,
        status: 200,
        url: `${ORIGIN}/bootstrap`,
      })
    );

    await expect(
      asyncFailureCode(() =>
        loadBrowserBootstrap({
          endpoint: "/bootstrap",
          expectedAudience: { origin: ORIGIN },
          fetch: fetch as unknown as typeof globalThis.fetch,
          projection: projection(),
          requestBaseUrl: ORIGIN,
        })
      )
    ).resolves.toBe("BOOTSTRAP_INVALID_JSON");
    expect(read).toHaveBeenCalledOnce();
    expect(cancel).toHaveBeenCalledOnce();
  });

  it("enforces exact body, split BOM, and UTF-8 precedence boundaries", async () => {
    const encoder = new TextEncoder();
    const source = envelope();
    const exact = encoder.encode(
      `${source}${" ".repeat(65_536 - encoder.encode(source).byteLength)}`
    );
    const oversized = Uint8Array.from([...exact, 0x20]);
    const exactResponse = responseForChunks([exact]);
    const oversizedResponse = responseForChunks([oversized]);

    await expect(
      loadFromResponse(exactResponse.response)
    ).resolves.toMatchObject({
      values: { apiOrigin: "https://api.example" },
    });
    expect(exactResponse.cancel).not.toHaveBeenCalled();
    await expect(
      asyncFailureCode(() => loadFromResponse(oversizedResponse.response))
    ).resolves.toBe("BOOTSTRAP_BODY_TOO_LARGE");
    expect(oversizedResponse.cancel).toHaveBeenCalledOnce();

    const splitBom = responseForChunks([
      Uint8Array.of(0xef),
      Uint8Array.of(0xbb),
      Uint8Array.from([0xbf, ...new Uint8Array(65_534)]),
    ]);
    await expect(
      asyncFailureCode(() => loadFromResponse(splitBom.response))
    ).resolves.toBe("BOOTSTRAP_INVALID_JSON");
    expect(splitBom.read).toHaveBeenCalledTimes(3);
    expect(splitBom.cancel).toHaveBeenCalledOnce();

    const invalidUtf8 = responseForChunks([Uint8Array.of(0xff)]);
    await expect(
      asyncFailureCode(() => loadFromResponse(invalidUtf8.response))
    ).resolves.toBe("BOOTSTRAP_INVALID_UTF8");
    expect(invalidUtf8.cancel).not.toHaveBeenCalled();

    const oversizedInvalidUtf8 = responseForChunks([
      Uint8Array.from([0xff, ...new Uint8Array(65_536)]),
    ]);
    await expect(
      asyncFailureCode(() => loadFromResponse(oversizedInvalidUtf8.response))
    ).resolves.toBe("BOOTSTRAP_BODY_TOO_LARGE");
    expect(oversizedInvalidUtf8.cancel).toHaveBeenCalledOnce();
  });

  it("maps hostile genuine Uint8Array chunk observations to body-read failure", async () => {
    const chunk = new Uint8Array([0x7b]);
    Object.defineProperty(chunk, "byteLength", {
      get(): never {
        throw new Error("must not escape");
      },
    });
    const fetch = vi.fn<() => Promise<Readonly<Record<string, unknown>>>>(() =>
      Promise.resolve({
        body: {
          getReader: () => ({
            cancel: () => Promise.resolve(),
            read: () => Promise.resolve({ done: false, value: chunk }),
          }),
        },
        headers: { get: () => "application/json" },
        redirected: false,
        status: 200,
        url: `${ORIGIN}/bootstrap`,
      })
    );
    await expect(
      asyncFailureCode(() =>
        loadBrowserBootstrap({
          endpoint: "/bootstrap",
          expectedAudience: { origin: ORIGIN },
          fetch: fetch as unknown as typeof globalThis.fetch,
          projection: projection(),
          requestBaseUrl: ORIGIN,
        })
      )
    ).resolves.toBe("BOOTSTRAP_BODY_READ_FAILED");
  });

  it("does not observe application loading until bootstrap succeeds", async () => {
    let importReads = 0;
    const options = {
      endpoint: "/bootstrap",
      expectedAudience: { origin: ORIGIN },
      fetch: vi.fn<() => Promise<Readonly<Record<string, unknown>>>>(() =>
        Promise.resolve(responseFor("{"))
      ) as unknown as typeof globalThis.fetch,
      get importApplication(): never {
        importReads += 1;
        throw new Error("application must remain deferred");
      },
      projection: projection(),
      requestBaseUrl: ORIGIN,
    };

    await expect(
      asyncFailureCode(() => startBrowserApplication(options))
    ).resolves.toBe("BOOTSTRAP_INVALID_JSON");
    expect(importReads).toBe(0);
  });

  it("starts with the exact owned bootstrap references", async () => {
    let importReceiver: unknown;
    let moduleReceiver: unknown;
    let startReads = 0;
    let receivedAudience: unknown;
    let receivedValues: unknown;
    const captureImportReceiver = (value: unknown): void => {
      importReceiver = value;
    };
    const captureModuleReceiver = (value: unknown): void => {
      moduleReceiver = value;
    };
    const application = {
      get start() {
        startReads += 1;
        return function start(
          this: unknown,
          values: Readonly<Configuration>,
          audience: Readonly<{ origin: string }>
        ): string {
          captureModuleReceiver(this);
          receivedValues = values;
          receivedAudience = audience;
          return values.apiOrigin;
        };
      },
    };
    const importApplication = function importApplication(this: unknown) {
      captureImportReceiver(this);
      return Promise.resolve(application);
    };

    const result = await startBrowserApplication({
      endpoint: "/bootstrap",
      expectedAudience: { origin: ORIGIN },
      fetch: vi.fn<() => Promise<Readonly<Record<string, unknown>>>>(() =>
        Promise.resolve(responseFor(envelope()))
      ) as unknown as typeof globalThis.fetch,
      importApplication,
      projection: projection(),
      requestBaseUrl: ORIGIN,
    });

    expect(result).toBe("https://api.example");
    expect(importReceiver).toBeUndefined();
    expect(startReads).toBe(1);
    expect(moduleReceiver).toBe(application);
    expect(Object.getPrototypeOf(receivedValues)).toBeNull();
    expect(receivedAudience).toStrictEqual({ origin: ORIGIN });
  });

  it("reads start hooks once and preserves post-bootstrap rejection identity", async () => {
    const importReason = Object.freeze({ stage: "import" });
    let importReads = 0;
    const importFailureOptions = {
      endpoint: "/bootstrap",
      expectedAudience: { origin: ORIGIN },
      fetch: vi.fn(() =>
        Promise.resolve(responseFor(envelope()))
      ) as unknown as typeof globalThis.fetch,
      get importApplication() {
        importReads += 1;
        return () => {
          throw importReason;
        };
      },
      projection: projection(),
      requestBaseUrl: ORIGIN,
    };

    await expect(startBrowserApplication(importFailureOptions)).rejects.toBe(
      importReason
    );
    expect(importReads).toBe(1);

    const startReason = Object.freeze({ stage: "start" });
    let secondImportReads = 0;
    let startReads = 0;
    const startFailureOptions = {
      endpoint: "/bootstrap",
      expectedAudience: { origin: ORIGIN },
      fetch: vi.fn(() =>
        Promise.resolve(responseFor(envelope()))
      ) as unknown as typeof globalThis.fetch,
      get importApplication() {
        secondImportReads += 1;
        return () =>
          Promise.resolve({
            get start() {
              startReads += 1;
              return () => Promise.reject(startReason);
            },
          });
      },
      projection: projection(),
      requestBaseUrl: ORIGIN,
    };

    await expect(startBrowserApplication(startFailureOptions)).rejects.toBe(
      startReason
    );
    expect(secondImportReads).toBe(1);
    expect(startReads).toBe(1);
  });
});
